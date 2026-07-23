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

	"blueshift/internal/media"
)

// Stage names a unit of pipeline work. The M1 pipeline is ingest -> transcribe
// -> diarize -> moments -> render; these constants name the whole set (they match
// the episodes.current_stage CHECK and the Library's five bars). Only the stages
// registered in defaultStages are actually *runnable* by the worker — M1 wires
// ingest then transcribe; the rest register as they land. Stage names are neutral
// product terms, never provider names.
type Stage string

const (
	// StageIngest extracts audio and renders a browser proxy from the master.
	StageIngest Stage = "ingest"
	// StageTranscribe turns the ingest audio into word-timed transcript segments
	// (internal/asr). It is registered after ingest, so ingest auto-advances into
	// it and transcribe is the terminal stage in this M1-partial pipeline.
	StageTranscribe Stage = "transcribe"
	// StageDiarize, StageMoments, StageRender name the remaining downstream stages.
	// They are declared here (and allowed by the DB CHECK) but are not yet
	// registered in defaultStages, so the worker refuses to run them until their
	// implementations land.
	StageDiarize Stage = "diarize"
	StageMoments Stage = "moments"
	StageRender  Stage = "render"
)

// stageDef is one entry in the ordered stage registry: a runnable stage's name
// and its per-attempt execution. The slice order defines the pipeline sequence
// and therefore auto-advance — each stage triggers the next in the slice, and the
// last registered stage is terminal (-> status ready).
type stageDef struct {
	name Stage
	// run executes one attempt of the stage against the runner's seams and returns
	// the outputs to persist on success (proxy key + measured duration for ingest;
	// later stages fill what they produce).
	run func(r *Runner, ctx context.Context, ep Episode, attempt int) (stageOutput, error)
}

// stageOutput is what a completed stage records on success. Today only ingest
// produces outputs (the browser proxy key and the measured duration); the
// finalize persists them the same way whether the stage is terminal (MarkReady)
// or intermediate (AdvanceStage).
type stageOutput struct {
	ProxyKey   string
	DurationMs int64
}

// defaultStages is the ordered registry of runnable stages. M1 registers ingest
// then transcribe: ingest is now an intermediate stage that auto-advances into
// transcribe, and transcribe is the terminal stage whose success flips the
// episode to 'ready' (the new M1-partial behaviour — the episode is Ready once it
// has a proxy AND a transcript). diarize/moments/render append here (in order) as
// they land, at which point transcribe becomes intermediate too.
var defaultStages = []stageDef{
	{name: StageIngest, run: (*Runner).runIngest},
	{name: StageTranscribe, run: (*Runner).runTranscribe},
}

// ValidStage reports whether s names a stage the worker can run (i.e. one
// registered in defaultStages). cmd/worker validates its argument against this,
// so a not-yet-implemented stage name is rejected up front.
func ValidStage(s string) bool {
	for _, st := range defaultStages {
		if st.name == Stage(s) {
			return true
		}
	}
	return false
}

// Episode is the claimed subject of a pipeline run: the identifiers needed to
// build org-owned storage keys plus the master to process. Ids are the encoded,
// prefixed public forms (org_…, ep_…) so key building matches m0-upload exactly.
type Episode struct {
	OrgID           string
	PublicID        string
	MasterObjectKey string
	Language        string
	// DurationMs is the media length ingest measured (ffprobe), 0 until ingest has
	// run. A continuation stage reads it as measured data — the transcribe stage
	// plans its ≤15-min chunk windows from it (verbatim invariant: the length is
	// measured, never guessed) instead of re-probing the audio object.
	DurationMs int64
}

// Repo is the pipeline's view of episode persistence. Claim is the stage-aware
// compare-and-set that takes an episode for a stage and stamps current_stage +
// claimed_at (a second concurrent/duplicate invocation sees claimed=false and
// no-ops); prevStage "" is an entry stage claimed from 'uploaded', a non-empty
// prevStage a continuation stage claimed only from a 'processing' episode at that
// predecessor stage. AdvanceStage is the intermediate finalize (record outputs,
// hand off, stay 'processing'); MarkReady/MarkFailed are the terminal finalizers.
// All are org-scoped and keyed by the org resolved during Claim, so a run can
// never write across tenants, and all are idempotent on a losing race.
type Repo interface {
	Claim(ctx context.Context, episodePublicID, stage, prevStage string) (ep Episode, claimed bool, err error)
	// AdvanceStage records a non-terminal stage's outputs and hands off to the next
	// stage while keeping the episode 'processing'. It is gated on
	// current_stage = completedStage (in addition to the org + 'processing' gate),
	// so it only ever finalizes the stage this run actually completed.
	AdvanceStage(ctx context.Context, orgID, episodePublicID, completedStage, proxyObjectKey string, durationMs int64) error
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
// only from measurement (verbatim invariant). Probe returns the structured
// summary that both measures the duration and drives the remux/transcode ruling;
// RemuxProxy is the stream-copy fast path for an already-compatible master and
// RenderProxy the transcode fallback.
type Media interface {
	Probe(ctx context.Context, path string) (media.ProbeResult, error)
	RemuxProxy(ctx context.Context, in, out string) error
	RenderProxy(ctx context.Context, in, out string) error
	ExtractAudio(ctx context.Context, in, out string) error
	// CutAudio writes the [startMs, startMs+durationMs) window of the audio at in
	// to out (mono 16 kHz FLAC). The transcribe stage uses it to split long audio
	// into ≤15-min chunks; short audio (a single chunk covering the whole track) is
	// transcribed directly and never calls this.
	CutAudio(ctx context.Context, in, out string, startMs, durationMs int) error
}

// Config tunes a run: the per-attempt timeout, how many extra attempts follow
// the first on failure, and the overall-bitrate budget under which an
// already-compatible master is remuxed rather than transcoded. StageTimeout<=0
// falls back to defaultStageTimeout; a negative Retries clamps to 0;
// MaxRemuxBitrate<=0 falls back to defaultMaxRemuxBitrate. cmd/worker sets all
// three explicitly from env, using DefaultRetries for the production 1+2 attempt
// budget.
type Config struct {
	StageTimeout time.Duration
	Retries      int
	// MaxRemuxBitrate is the overall-bitrate ceiling (bits/sec) for the remux fast
	// path (config PROXY_MAX_REMUX_BITRATE). A master above it is transcoded so a
	// proxy always streams cheaply.
	MaxRemuxBitrate int64
	// AutoAdvance triggers the next registered stage when a non-terminal stage
	// succeeds. It maps to PIPELINE_AUTO_ADVANCE (default true); cmd/worker sets it
	// from config. When false, a completed non-terminal stage still records its
	// handoff durably, but the next stage is not launched — a staged rollout /
	// manual-drive mode. It has no effect on a terminal stage (there is no next).
	AutoAdvance bool
	// TranscribeChunkMs caps the transcribe stage's per-chunk audio length in
	// milliseconds. <=0 uses defaultTranscribeChunkMs (15 min, a margin under the
	// ~20-min word-timestamp batch limit). Exists so a test can force multi-chunk
	// splitting on a short fixture; production leaves it at the default.
	TranscribeChunkMs int
}

const (
	defaultStageTimeout = 30 * time.Minute
	// defaultMaxRemuxBitrate is the fallback remux bitrate ceiling (~6 Mbps) when
	// Config.MaxRemuxBitrate is unset. It mirrors config.defaultProxyMaxRemuxBitrate;
	// in production the value flows from PROXY_MAX_REMUX_BITRATE via cmd/worker, so
	// this is only a safety net for a Config that omits it.
	defaultMaxRemuxBitrate = 6_000_000
	// DefaultRetries is the number of *additional* attempts after the first, per
	// the ruling (total attempts = 1 + retries = 3). cmd/worker applies it.
	DefaultRetries = 2
	// defaultTranscribeChunkMs is the transcribe stage's per-chunk audio cap when
	// Config.TranscribeChunkMs is unset: 15 minutes, a deliberate margin under the
	// ~20-min limit the batch API imposes when word-level timestamps are enabled
	// (see internal/asr/stitch.go). Audio at or under it is one chunk.
	defaultTranscribeChunkMs = 15 * 60 * 1000
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

func (c Config) maxRemuxBitrate() int64 {
	if c.MaxRemuxBitrate <= 0 {
		return defaultMaxRemuxBitrate
	}
	return c.MaxRemuxBitrate
}

func (c Config) transcribeChunkMs() int {
	if c.TranscribeChunkMs <= 0 {
		return defaultTranscribeChunkMs
	}
	return c.TranscribeChunkMs
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
	// Trigger launches the next stage on auto-advance (the same neutral trigger
	// seam the API server uses; the worker's SA already holds the runner role). It
	// is only consulted when a non-terminal stage succeeds and Config.AutoAdvance
	// is set. Nil means no trigger is configured: the handoff is recorded but the
	// next stage is not launched (logged; recoverable by a manual re-drive or the
	// stale-claim sweep).
	Trigger Trigger
	// ASR resolves the speech engine bound to an episode's content language (the
	// lang registry declares the language uses an asr slot; a neutral label binds
	// it to a registered engine). Only the transcribe stage consults it; an
	// ingest-only worker may leave it nil. Provider choice never crosses this seam.
	ASR ASR
	// Segments persists an episode's transcript (idempotent, org-scoped). Only the
	// transcribe stage consults it; nil for an ingest-only worker.
	Segments SegmentStore
	// stages overrides the default stage registry. Nil uses defaultStages
	// (production). It exists so tests can register fake multi-stage pipelines to
	// exercise auto-advance without a real downstream stage; it is never set in
	// production wiring.
	stages []stageDef
}

// registry returns the runner's effective, ordered stage registry: the injected
// override for tests, else the package default.
func (r *Runner) registry() []stageDef {
	if r.stages != nil {
		return r.stages
	}
	return defaultStages
}

// lookupStage finds a stage by name in the runner's registry, returning its
// definition and index. found=false when the name is not a runnable stage.
func (r *Runner) lookupStage(name Stage) (def stageDef, idx int, found bool) {
	for i, st := range r.registry() {
		if st.name == name {
			return st, i, true
		}
	}
	return stageDef{}, -1, false
}

// prevStageName returns the name of the stage before index idx, or "" when idx is
// the entry stage (index 0). It is the predecessor the continuation claim guards
// on.
func (r *Runner) prevStageName(idx int) string {
	if idx <= 0 {
		return ""
	}
	return string(r.registry()[idx-1].name)
}

// nextStageName returns the stage after index idx and whether one exists. No next
// stage means idx is terminal (its success flips the episode to 'ready').
func (r *Runner) nextStageName(idx int) (Stage, bool) {
	reg := r.registry()
	if idx < 0 || idx+1 >= len(reg) {
		return "", false
	}
	return reg[idx+1].name, true
}

// Run claims the episode and executes stage, driving the status machine. A
// concurrent invocation that loses the claim returns nil (a clean no-op, exit
// 0). On success the episode is 'ready'; on exhausted attempts it is 'failed'
// with a neutral error_id and Run returns ErrStageFailed.
func (r *Runner) Run(ctx context.Context, episodePublicID, stage string) error {
	log := r.logger()
	def, idx, ok := r.lookupStage(Stage(stage))
	if !ok {
		return fmt.Errorf("pipeline: unknown stage %q", stage)
	}

	started := time.Now()
	prev := r.prevStageName(idx)
	ep, claimed, err := r.Repo.Claim(ctx, episodePublicID, stage, prev)
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
		out, aerr := def.run(r, ctx, ep, attempt)
		if aerr == nil {
			return r.finalizeSuccess(ctx, ep, stage, idx, out, attempt)
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

// finalizeSuccess records a stage's success and drives the transition. If the
// stage is terminal (the last registered stage — ingest today), it flips the
// episode to 'ready' via MarkReady, exactly the M0 single-stage behaviour. If a
// next stage is registered it is a non-terminal stage: AdvanceStage records the
// outputs and hands off while the episode stays 'processing', then — only when
// Config.AutoAdvance is set — the next stage is launched via the Trigger seam.
// Auto-advance can never loop or skip: the trigger is fired for exactly the next
// registered stage, and the store's continuation claim only accepts it from this
// stage as predecessor.
func (r *Runner) finalizeSuccess(ctx context.Context, ep Episode, stage string, idx int, out stageOutput, attempt int) error {
	log := r.logger()
	next, hasNext := r.nextStageName(idx)
	if !hasNext {
		// Terminal stage: record outputs and mark ready.
		if err := r.Repo.MarkReady(ctx, ep.OrgID, ep.PublicID, out.ProxyKey, out.DurationMs); err != nil {
			return fmt.Errorf("pipeline: mark ready: %w", err)
		}
		log.InfoContext(ctx, "stage complete",
			slog.String("episode", ep.PublicID), slog.String("stage", stage),
			slog.Int("attempt", attempt), slog.Int64("duration_ms", out.DurationMs),
			slog.Bool("terminal", true))
		return nil
	}

	// Non-terminal stage: record outputs and hand off, staying 'processing'.
	if err := r.Repo.AdvanceStage(ctx, ep.OrgID, ep.PublicID, stage, out.ProxyKey, out.DurationMs); err != nil {
		return fmt.Errorf("pipeline: advance stage: %w", err)
	}
	log.InfoContext(ctx, "stage complete",
		slog.String("episode", ep.PublicID), slog.String("stage", stage),
		slog.Int("attempt", attempt), slog.Int64("duration_ms", out.DurationMs),
		slog.String("next_stage", string(next)), slog.Bool("terminal", false))

	if !r.Config.AutoAdvance {
		log.InfoContext(ctx, "auto-advance disabled; next stage awaits manual trigger",
			slog.String("episode", ep.PublicID), slog.String("next_stage", string(next)))
		return nil
	}
	if r.Trigger == nil {
		log.WarnContext(ctx, "auto-advance enabled but no trigger configured; next stage not launched",
			slog.String("episode", ep.PublicID), slog.String("next_stage", string(next)))
		return nil
	}
	if err := r.Trigger.Trigger(ctx, ep.PublicID, string(next)); err != nil {
		// Best-effort: the handoff is already durable, so a trigger miss must not
		// fail an episode whose stage actually succeeded. Log it (neutrally, with a
		// correlation id); a manual re-drive or the stale-claim sweep recovers it.
		id := newErrorID()
		log.ErrorContext(ctx, "auto-advance trigger failed",
			slog.String("episode", ep.PublicID), slog.String("next_stage", string(next)),
			slog.String("error_id", id), slog.String("error", err.Error()))
		return nil
	}
	log.InfoContext(ctx, "auto-advanced to next stage",
		slog.String("episode", ep.PublicID), slog.String("next_stage", string(next)))
	return nil
}

// runIngest adapts the ingest stage to the registry's run signature: it runs one
// ingest attempt and reports the proxy key + measured duration as the stage's
// outputs. The heavy lifting stays in attemptIngest (ingest.go).
func (r *Runner) runIngest(ctx context.Context, ep Episode, attempt int) (stageOutput, error) {
	proxyKey, durationMs, err := r.attemptIngest(ctx, ep, attempt)
	if err != nil {
		return stageOutput{}, err
	}
	return stageOutput{ProxyKey: proxyKey, DurationMs: durationMs}, nil
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
