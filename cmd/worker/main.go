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

	"blueshift/internal/blob"
	"blueshift/internal/config"
	"blueshift/internal/logx"
	"blueshift/internal/media"
	"blueshift/internal/pipeline"
	"blueshift/internal/store"
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
		return fmt.Errorf("unknown stage %q (want ingest)", stage)
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
		},
		// The worker launches the next stage on auto-advance through the same
		// neutral trigger the API server uses (its SA already holds the runner
		// role). With only ingest registered this is dormant — ingest is terminal —
		// but wiring it keeps the multi-stage machinery complete.
		Trigger: buildTrigger(cfg, logger),
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
