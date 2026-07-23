// Package sweep runs the app-side abandoned-upload reaper: a periodic,
// system-level TTL sweep that removes episodes a create left at 'uploaded' with
// no master key whose client never completed the upload. No queue, no pg_cron —
// a single goroutine ticker owned by the API server (Occam), disabled entirely
// when no database is configured.
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

// Repo is the persistence port the sweep needs: a single system-level delete of
// abandoned uploads older than ttl, returning the number removed. *store.Store
// satisfies it.
type Repo interface {
	SweepAbandonedEpisodes(ctx context.Context, ttl time.Duration) (int64, error)
}

// Run drives the sweep on a ticker until ctx is cancelled, then returns. It
// blocks, so callers launch it in a goroutine bound to the app's lifecycle
// context. The first sweep fires after min(DefaultFirstDelay, interval); every
// subsequent sweep fires one interval after the previous one completes. A failed
// sweep is logged (server-side only) and the loop continues — a transient DB
// blip must not kill the reaper.
func Run(ctx context.Context, repo Repo, logger *slog.Logger, interval, ttl time.Duration) {
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
			sweepOnce(ctx, repo, logger, ttl)
			// ctx may have been cancelled while the sweep ran; reset then let the
			// next select observe Done() so we never schedule after shutdown.
			timer.Reset(interval)
		}
	}
}

// sweepOnce runs a single sweep, logging a non-zero result at INFO and any
// failure at ERROR. Both stay in the server logs; nothing here is client-facing.
func sweepOnce(ctx context.Context, repo Repo, logger *slog.Logger, ttl time.Duration) {
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
