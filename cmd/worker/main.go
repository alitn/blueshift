// Command worker is the pipeline entrypoint for Cloud Run Jobs. It runs one
// stage for one episode and exits: 0 on success or a clean no-op (a concurrent
// worker already claimed the episode), 1 on a stage failure or a setup fault —
// the exit code the Jobs runtime uses to mark the execution. On SIGTERM (the
// stop signal Cloud Run sends before force-killing a task) it cancels the run,
// lets the pipeline mark the claimed episode 'failed' within the grace window,
// and exits non-zero — never leaving the episode stuck in 'processing'. All logic
// lives in internal/pipeline; this main is wiring only.
//
//	worker <episode_public_id> <stage>
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"blueshift/internal/asr"
	"blueshift/internal/blob"
	"blueshift/internal/config"
	"blueshift/internal/logx"
	"blueshift/internal/media"
	"blueshift/internal/pipeline"
	"blueshift/internal/store"

	// Register the supported content languages with the /internal/lang registry
	// (import for side effect). The transcribe stage resolves an episode's language
	// through the registry, so each content language must be registered here.
	// Persian (fa) is the first; additional languages add a blank import.
	_ "blueshift/internal/lang/fa"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: worker <episode_public_id> <stage>")
	}
	episodeID, stage := args[0], args[1]
	if !pipeline.ValidStage(stage) {
		return fmt.Errorf("unknown stage %q (want ingest|transcribe)", stage)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := logx.New(cfg.LogLevel, os.Stdout)
	logger.Info("worker starting", "episode", episodeID, "stage", stage, "env", string(cfg.Env))

	if cfg.DatabaseURL == "" {
		return fmt.Errorf("worker: DATABASE_URL is required")
	}

	// Trap the stop signals Cloud Run sends a task before it force-kills the
	// container (SIGTERM, ~10s grace) so the whole run — the stage context and the
	// ffmpeg child under internal/media — is cancelled cleanly. The pipeline then
	// marks the claimed episode 'failed' on a detached, bounded context inside that
	// grace window, so a timed-out/pre-empted attempt never leaves the episode
	// stuck in 'processing'. cancel() is deferred so the signal handler is released
	// on exit.
	ctx, cancel := shutdownContext()
	defer cancel()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("worker: store: %w", err)
	}
	defer st.Close()

	bs, err := buildBlob(ctx, cfg)
	if err != nil {
		return fmt.Errorf("worker: blob: %w", err)
	}

	runner := &pipeline.Runner{
		Repo:  st,
		Blob:  bs,
		Media: media.Runner{},
		Log:   logger,
		Config: pipeline.Config{
			StageTimeout:    cfg.IngestTimeout,
			Retries:         pipeline.DefaultRetries,
			MaxRemuxBitrate: cfg.ProxyMaxRemuxBitrate,
			AutoAdvance:     cfg.PipelineAutoAdvance,
			// Cost-safety: the per-episode billable-attempt ceiling and the deliberate
			// reprocess override (CLAUDE.md "Billable-service cost safety"). Dormant
			// under the default ingest-only chain (ingest is not billable); they bound
			// the metered ASR / LLM cost once PIPELINE_STAGES activates a billable stage.
			MaxProcessAttempts: cfg.MaxProcessAttempts,
			Reprocess:          cfg.Reprocess,
		},
		// The worker launches the next stage on auto-advance through the same
		// neutral trigger the API server uses (its SA already holds the runner
		// role). Dormant under the default ingest-only chain (ingest is terminal);
		// active once PIPELINE_STAGES adds a downstream stage.
		Trigger: buildTrigger(cfg, logger),
	}

	// Install the config-driven active stage chain (PIPELINE_STAGES, default
	// ingest-only). Validate at startup so a misconfigured chain fails fast before
	// any claim, never stalling an episode mid-pipeline.
	if err := runner.SetActiveStages(toStages(cfg.PipelineStages)); err != nil {
		return fmt.Errorf("worker: pipeline stages: %w", err)
	}

	// Only the transcribe stage consults the speech engine and the segment store,
	// so build them just for a chain that includes transcribe. An ingest-only
	// worker (the default, and a prod worker without ASR config) needs no ASR
	// configuration and stays ingest-terminal — matching the runner's nil-ASR
	// contract. The lang registry declaration + neutral label bind the engine to a
	// language; the transcript persists org-scoped and idempotent.
	if runner.HasStage(pipeline.StageTranscribe) {
		asrRegistry, err := buildASRRegistry(cfg, logger)
		if err != nil {
			return fmt.Errorf("worker: asr: %w", err)
		}
		runner.ASR = pipeline.LangEngineResolver{Registry: asrRegistry, Label: cfg.ASREngineLabel}
		runner.Segments = st
	}

	if err := runner.Run(ctx, episodeID, stage); err != nil {
		// The episode is already recorded 'failed' with a neutral error_id; the
		// raw cause is in the logs above. Returning the error exits nonzero so the
		// Jobs runtime marks the execution failed.
		return err
	}
	logger.Info("worker done", "episode", episodeID, "stage", stage)
	return nil
}

// toStages converts the configured stage-name list (PIPELINE_STAGES) to the
// pipeline's Stage type. An empty list returns nil, leaving the runner on its
// default ingest-only chain.
func toStages(names []string) []pipeline.Stage {
	if len(names) == 0 {
		return nil
	}
	out := make([]pipeline.Stage, len(names))
	for i, n := range names {
		out[i] = pipeline.Stage(n)
	}
	return out
}

// shutdownContext returns a context cancelled on the first SIGINT or SIGTERM.
// SIGTERM is what Cloud Run sends a task attempt when it hits its task-timeout or
// is otherwise pre-empted, ~10s before SIGKILL (container runtime contract); the
// returned cancel releases the signal handler. Extracted so the signal wiring is
// unit-testable without booting the full worker.
func shutdownContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// buildBlob constructs the object-storage backend for the configured mode,
// mirroring cmd/app so the worker reads and writes the same layout. The local
// store signs with the session secret (unused by the worker's read/write path
// but required by the constructor).
func buildBlob(ctx context.Context, cfg config.Config) (pipeline.Blob, error) {
	switch cfg.BlobMode {
	case config.BlobModeGCS:
		return blob.NewGCS(ctx, cfg.GCSBucket)
	default:
		return blob.NewLocal(cfg.BlobDir, []byte(cfg.SessionSecret), nil)
	}
}

// buildASRRegistry constructs the speech-recognition registry the transcribe
// stage resolves engines from. The mode selects which engine backs the neutral
// label: `fake` replays the embedded offline recordings (dev/demo, deterministic,
// no credential); `speech` is the provider-backed engine fully specified by
// SpeechConfig (region/model/bucket/project from env per docs/RUNBOOK.md — the
// engine constructor validates requiredness, so a misconfigured speech worker
// fails fast here). Provider names are confined to internal/asr and the deploy
// env; nothing they touch is client-visible.
func buildASRRegistry(cfg config.Config, logger *slog.Logger) (*asr.Registry, error) {
	var (
		engine asr.Engine
		err    error
	)
	switch cfg.ASRMode {
	case config.ASRModeSpeech:
		engine, err = asr.NewSpeechEngine(asr.SpeechConfig{
			Label:         cfg.ASREngineLabel,
			Model:         cfg.ASRModel,
			Region:        cfg.ASRRegion,
			Project:       cfg.ASRProject,
			Bucket:        cfg.ASRBucket,
			LanguageCodes: cfg.ASRLanguageCodes,
			Logger:        logger,
		})
	default:
		engine, err = asr.NewDefaultFakeEngine(cfg.ASREngineLabel)
	}
	if err != nil {
		return nil, err
	}
	return asr.NewRegistry(engine)
}

// buildTrigger constructs the worker's own trigger for auto-advancing to the next
// stage, mirroring cmd/app's mode selection: cloudrun starts a Cloud Run Jobs
// execution, exec spawns the worker binary again. In exec mode it prefers the
// configured WORKER_BIN and falls back to this running binary (os.Args[0]) so a
// dev/demo worker can always re-invoke itself. The provider detail stays inside
// internal/pipeline.
func buildTrigger(cfg config.Config, logger *slog.Logger) pipeline.Trigger {
	switch cfg.WorkerTrigger {
	case config.WorkerTriggerCloudRun:
		return pipeline.NewCloudRunTrigger(cfg.WorkerJobProject, cfg.WorkerJobRegion, cfg.WorkerJobName, logger)
	default:
		bin := cfg.WorkerBin
		if bin == "" {
			bin = os.Args[0]
		}
		return pipeline.NewExecTrigger(bin, logger)
	}
}
