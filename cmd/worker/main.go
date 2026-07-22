// Command worker is the pipeline entrypoint for Cloud Run Jobs. It runs one
// stage for one episode and exits: 0 on success or a clean no-op (a concurrent
// worker already claimed the episode), 1 on a stage failure or a setup fault —
// the exit code the Jobs runtime uses to mark the execution. All logic lives in
// internal/pipeline; this main is wiring only.
//
//	worker <episode_public_id> <stage>
package main

import (
	"context"
	"fmt"
	"os"

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

	ctx := context.Background()
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
			StageTimeout: cfg.IngestTimeout,
			Retries:      pipeline.DefaultRetries,
		},
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
