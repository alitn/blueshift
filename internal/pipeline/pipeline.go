// Package pipeline is the worker's brain: it owns the episode status machine and
// the per-stage claim/retry/timeout/finalize logic that turns an uploaded master
// into ready renders. `cmd/worker` is a thin main around Runner.Run; the media
// work lives behind the Media seam (internal/media) and byte movement behind the
// Blob seam (internal/blob), so the whole flow is exercised offline with fakes.
//
// Vendor neutrality holds here too: a stage failure is logged with its raw cause
// server-side and recorded on the episode as a neutral error_id (random hex) —
// never a provider- or tool-named message a client could see.
package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// Stage names a unit of pipeline work. M0 defines exactly one; transcribe and
// analyze arrive with M1. Kept as a small closed set validated at dispatch.
type Stage string

// StageIngest extracts audio and renders a browser proxy from the master.
const StageIngest Stage = "ingest"

// knownStages is the closed registry of runnable stages.
var knownStages = map[Stage]struct{}{
	StageIngest: {},
}

// ValidStage reports whether s names a runnable stage.
func ValidStage(s string) bool {
	_, ok := knownStages[Stage(s)]
	return ok
}

// Episode is the claimed subject of a pipeline run: the identifiers needed to
// build org-owned storage keys plus the master to process. Ids are the encoded,
// prefixed public forms (org_…, ep_…) so key building matches m0-upload exactly.
type Episode struct {
	OrgID           string
	PublicID        string
	MasterObjectKey string
	Language        string
}

// Repo is the pipeline's view of episode persistence. Claim is the compare-and-
// set that advances a single 'uploaded' episode to 'processing' (a second
// concurrent invocation sees claimed=false and no-ops). MarkReady/MarkFailed are
// org-scoped finalizers keyed by the org resolved during Claim, so a run can
// never write across tenants. All are idempotent on a losing race.
type Repo interface {
	Claim(ctx context.Context, episodePublicID string) (ep Episode, claimed bool, err error)
	MarkReady(ctx context.Context, orgID, episodePublicID, proxyObjectKey string, durationMs int64) error
	MarkFailed(ctx context.Context, orgID, episodePublicID, errorID string) error
	// EpisodeStatus reports an episode's current status, used only to annotate the
	// WARN logged when a claim is refused (the blocking status). "" means the id
	// names no episode. It never scopes by org (the worker has no org pre-claim).
	EpisodeStatus(ctx context.Context, episodePublicID string) (status string, err error)
}

// Blob is the byte-movement contract the pipeline needs. The remote (GCS)
// backend implements only these two methods: the pipeline downloads the master
// into a work dir and uploads the renders. A filesystem backend additionally
// implements localPather, letting the pipeline run ffmpeg on objects in place.
type Blob interface {
	Download(ctx context.Context, key, destPath string) error
	Upload(ctx context.Context, key, srcPath, contentType string) error
}

// localPather is the optional extension a filesystem-backed Blob implements so
// the pipeline operates on objects in place (dev/demo "direct paths" mode) with
// no copy in or out.
type localPather interface {
	LocalPath(key string) (string, error)
}

// Media is the ffmpeg/ffprobe seam (internal/media). Behind it, durations come
// only from measurement (verbatim invariant).
type Media interface {
	ProbeDuration(ctx context.Context, path string) (time.Duration, error)
	RenderProxy(ctx context.Context, in, out string) error
	ExtractAudio(ctx context.Context, in, out string) error
}

// Config tunes a run: the per-attempt timeout and how many extra attempts follow
// the first on failure. StageTimeout<=0 falls back to defaultStageTimeout; a
// negative Retries clamps to 0. cmd/worker sets both explicitly from env, using
// DefaultRetries for the production 1+2 attempt budget.
type Config struct {
	StageTimeout time.Duration
	Retries      int
}

const (
	defaultStageTimeout = 30 * time.Minute
	// DefaultRetries is the number of *additional* attempts after the first, per
	// the ruling (total attempts = 1 + retries = 3). cmd/worker applies it.
	DefaultRetries = 2
	// shutdownFinalizeTimeout bounds the detached mark-failed write performed when
	// the run's context is already cancelled (SIGTERM shutdown / deadline). It must
	// stay well under Cloud Run's ~10s SIGTERM-to-SIGKILL grace so the episode is
	// durably marked 'failed' before the container is force-killed.
	shutdownFinalizeTimeout = 5 * time.Second
)

func (c Config) stageTimeout() time.Duration {
	if c.StageTimeout <= 0 {
		return defaultStageTimeout
	}
	return c.StageTimeout
}

func (c Config) retries() int {
	if c.Retries < 0 {
		return 0
	}
	return c.Retries
}

// ErrStageFailed reports that a stage exhausted its attempts. The episode is
// already recorded 'failed' with a neutral error_id by the time it surfaces;
// cmd/worker maps it to exit code 1 for Cloud Run Jobs semantics.
var ErrStageFailed = errors.New("pipeline: stage failed after retries")

// Runner executes stages against the injected seams. It holds no per-run state,
// so a single Runner is safe for concurrent Run calls.
type Runner struct {
	Repo   Repo
	Blob   Blob
	Media  Media
	Log    *slog.Logger
	Config Config
}

// Run claims the episode and executes stage, driving the status machine. A
// concurrent invocation that loses the claim returns nil (a clean no-op, exit
// 0). On success the episode is 'ready'; on exhausted attempts it is 'failed'
// with a neutral error_id and Run returns ErrStageFailed.
func (r *Runner) Run(ctx context.Context, episodePublicID, stage string) error {
	log := r.logger()
	if !ValidStage(stage) {
		return fmt.Errorf("pipeline: unknown stage %q", stage)
	}
	if stage != string(StageIngest) {
		// Defensive: only ingest is wired in M0.
		return fmt.Errorf("pipeline: stage %q not runnable in this milestone", stage)
	}

	started := time.Now()
	ep, claimed, err := r.Repo.Claim(ctx, episodePublicID)
	if err != nil {
		return fmt.Errorf("pipeline: claim: %w", err)
	}
	if !claimed {
		// A refused claim is a signal, not a success: a retry attempt (or a killed
		// attempt's automatic re-run) observing an episode it cannot take must not
		// masquerade as a clean no-op. Log it at WARN with the blocking status so
		// the "stuck in processing" failure mode is visible in the logs, not
		// silent. The status lookup is best-effort (server-log-only).
		blocking, serr := r.Repo.EpisodeStatus(ctx, episodePublicID)
		if serr != nil || blocking == "" {
			blocking = "unknown"
		}
		log.WarnContext(ctx, "episode not claimable; no-op",
			slog.String("episode", episodePublicID), slog.String("stage", stage),
			slog.String("blocking_status", blocking))
		return nil
	}
	log.InfoContext(ctx, "stage claimed",
		slog.String("episode", ep.PublicID), slog.String("stage", stage))

	attempts := 1 + r.Config.retries()
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		proxyKey, durationMs, aerr := r.attemptIngest(ctx, ep, attempt)
		if aerr == nil {
			if err := r.Repo.MarkReady(ctx, ep.OrgID, ep.PublicID, proxyKey, durationMs); err != nil {
				return fmt.Errorf("pipeline: mark ready: %w", err)
			}
			log.InfoContext(ctx, "stage complete",
				slog.String("episode", ep.PublicID), slog.String("stage", stage),
				slog.Int("attempt", attempt), slog.Int64("duration_ms", durationMs))
			return nil
		}
		lastErr = aerr
		// The raw cause (which may name codecs/tools) stays in server logs only.
		log.WarnContext(ctx, "stage attempt failed",
			slog.String("episode", ep.PublicID), slog.String("stage", stage),
			slog.Int("attempt", attempt), slog.Int("attempts", attempts),
			slog.String("error", aerr.Error()))
		// Abort the retry loop if the parent context is done (timeout/shutdown of
		// the whole run, not just one attempt).
		if ctx.Err() != nil {
			break
		}
	}

	errID := newErrorID()
	elapsed := time.Since(started)
	// Record the failure durably. When the run's context is already done — a
	// SIGTERM shutdown (Cloud Run's ~10s grace before SIGKILL) or a whole-run
	// deadline — the DB write must NOT ride that dead context, or it aborts and
	// leaves the episode stuck in 'processing' forever: exactly the live incident
	// this task fixes. Detach a fresh, bounded context so the mark-failed lands
	// well inside the grace window.
	shutdown := ctx.Err() != nil
	finCtx := ctx
	if shutdown {
		var cancel context.CancelFunc
		finCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), shutdownFinalizeTimeout)
		defer cancel()
	}
	if err := r.Repo.MarkFailed(finCtx, ep.OrgID, ep.PublicID, errID); err != nil {
		return fmt.Errorf("pipeline: mark failed: %w", err)
	}
	if shutdown {
		// A shutdown/timeout is operationally distinct from a stage that exhausted
		// its retries — log it as such (WARN, with the cause and elapsed) so the
		// two are not conflated in the logs.
		log.WarnContext(finCtx, "run aborted; episode marked failed",
			slog.String("episode", ep.PublicID), slog.String("stage", stage),
			slog.String("error_id", errID), slog.String("elapsed", elapsed.String()),
			slog.String("reason", ctx.Err().Error()))
	} else {
		log.ErrorContext(ctx, "stage failed after retries",
			slog.String("episode", ep.PublicID), slog.String("stage", stage),
			slog.String("error_id", errID), slog.Int("attempts", attempts),
			slog.String("elapsed", elapsed.String()), slog.String("error", errString(lastErr)))
	}
	return fmt.Errorf("%w (error_id=%s)", ErrStageFailed, errID)
}

func (r *Runner) logger() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// newErrorID returns a short random hex id that correlates a client-visible
// failure with the server log line holding the raw cause. It names nothing.
func newErrorID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// tempDir creates a fresh work directory for one stage attempt. Each attempt
// gets its own so a partial render from a failed attempt never contaminates the
// next (per the ruling).
func tempDir(attempt int) (string, func(), error) {
	dir, err := os.MkdirTemp("", fmt.Sprintf("bs-ingest-%d-*", attempt))
	if err != nil {
		return "", func() {}, fmt.Errorf("pipeline: temp dir: %w", err)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}
