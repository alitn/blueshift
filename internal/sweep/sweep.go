// Package sweep runs the app-side maintenance reaper: a periodic, system-level
// sweep that (1) removes episodes a create left at 'uploaded' with no master key
// whose client never completed the upload, and (2) force-fails episodes stuck at
// 'processing' whose worker was killed mid-stage (a stale claim), so nothing is
// ever wedged in a non-retryable state. No queue, no pg_cron — a single goroutine
// ticker owned by the API server (Occam), disabled entirely when no database is
// configured.
package sweep

import (
	"context"
	"log/slog"
	"time"
)

// DefaultFirstDelay is the grace period before the first sweep after boot. It
// keeps the reaper out of the startup hot path (the app is serving /healthz and
// the UI well before this fires) while still cleaning up within a couple of
// minutes of a restart. A shorter configured interval wins, so a fast transient
// env sweeps promptly.
const DefaultFirstDelay = time.Minute

// Repo is the persistence port the sweep needs: the two system-level sweeps,
// each returning the number of rows it affected. *store.Store satisfies it.
type Repo interface {
	// SweepAbandonedEpisodes hard-deletes uploads abandoned longer than ttl.
	SweepAbandonedEpisodes(ctx context.Context, ttl time.Duration) (int64, error)
	// SweepStuckProcessingEpisodes force-fails 'processing' claims older than ttl
	// (or with a NULL claimed_at — a legacy claim), the backstop for a killed
	// worker.
	SweepStuckProcessingEpisodes(ctx context.Context, ttl time.Duration) (int64, error)
}

// Run drives the sweep on a ticker until ctx is cancelled, then returns. It
// blocks, so callers launch it in a goroutine bound to the app's lifecycle
// context. The first sweep fires after min(DefaultFirstDelay, interval); every
// subsequent sweep fires one interval after the previous one completes. A failed
// sweep is logged (server-side only) and the loop continues — a transient DB
// blip must not kill the reaper. Each tick runs both sub-sweeps: abandoned
// uploads (uploadTTL) and stale 'processing' claims (processingTTL).
func Run(ctx context.Context, repo Repo, logger *slog.Logger, interval, uploadTTL, processingTTL time.Duration) {
	firstDelay := DefaultFirstDelay
	if interval < firstDelay {
		firstDelay = interval
	}

	timer := time.NewTimer(firstDelay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			sweepOnce(ctx, repo, logger, uploadTTL, processingTTL)
			// ctx may have been cancelled while the sweep ran; reset then let the
			// next select observe Done() so we never schedule after shutdown.
			timer.Reset(interval)
		}
	}
}

// sweepOnce runs both sub-sweeps once. They are independent: a failure in one
// must not skip the other, so each is a self-contained call.
func sweepOnce(ctx context.Context, repo Repo, logger *slog.Logger, uploadTTL, processingTTL time.Duration) {
	sweepAbandonedUploads(ctx, repo, logger, uploadTTL)
	sweepStuckProcessing(ctx, repo, logger, processingTTL)
}

// sweepAbandonedUploads removes uploads abandoned past ttl, logging a non-zero
// result at INFO (routine client abandonment) and any failure at ERROR. Both
// stay in the server logs; nothing here is client-facing.
func sweepAbandonedUploads(ctx context.Context, repo Repo, logger *slog.Logger, ttl time.Duration) {
	n, err := repo.SweepAbandonedEpisodes(ctx, ttl)
	if err != nil {
		logger.LogAttrs(ctx, slog.LevelError, "abandoned-upload sweep failed",
			slog.String("error", err.Error()))
		return
	}
	if n > 0 {
		logger.LogAttrs(ctx, slog.LevelInfo, "swept abandoned uploads",
			slog.Int64("count", n), slog.String("ttl", ttl.String()))
	}
}

// sweepStuckProcessing force-fails 'processing' claims older than ttl. A non-zero
// result is logged at WARN, not INFO: a stuck claim means a worker was killed
// mid-stage (SIGKILL/OOM/crash) or a legacy claim was left over — a signal worth
// surfacing, unlike routine upload abandonment. A failure is logged at ERROR.
func sweepStuckProcessing(ctx context.Context, repo Repo, logger *slog.Logger, ttl time.Duration) {
	n, err := repo.SweepStuckProcessingEpisodes(ctx, ttl)
	if err != nil {
		logger.LogAttrs(ctx, slog.LevelError, "stuck-processing sweep failed",
			slog.String("error", err.Error()))
		return
	}
	if n > 0 {
		logger.LogAttrs(ctx, slog.LevelWarn, "swept stuck processing episodes",
			slog.Int64("count", n), slog.String("ttl", ttl.String()))
	}
}
