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
// the episodes.current_stage CHECK and the Library's five bars). A stage is
// *runnable* once it is in the stage registry (stageRegistry) — M1 registers
// ingest and transcribe; the rest register as they land. Which registered stages
// actually run, and in what order, is the config-driven *active chain*
// (PIPELINE_STAGES, default ingest-only — see defaultActiveStages). Stage names
// are neutral product terms, never provider names.
type Stage string

const (
	// StageIngest extracts audio and renders a browser proxy from the master.
	StageIngest Stage = "ingest"
	// StageTranscribe turns the ingest audio into word-timed transcript segments
	// (internal/asr). It is registered (runnable) but joins the active chain only
	// when PIPELINE_STAGES names it; under the default ingest-only chain it stays
	// out of the chain — so a worker with no ASR config is ingest-terminal — with
	// its code and tests intact for the moment it is re-enabled.
	StageTranscribe Stage = "transcribe"
	// StageDiarize assigns an episode-local speaker label to each transcript
	// segment via the LLM seam, text-anchored (internal/diarize + internal/llm). It
	// is registered (runnable) but, like transcribe, PARKED — it joins the active
	// chain only when PIPELINE_STAGES names it; under the default ingest-only chain
	// it stays out of the chain with its code and tests intact.
	StageDiarize Stage = "diarize"
	// StageMoments, StageRender name the remaining downstream stages. They are
	// declared here (and allowed by the DB CHECK) but are not yet in the stage
	// registry, so the worker refuses to run them until their implementations land.
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

// stageRegistry is the full set of runnable stages the worker knows how to
// execute, in declaration order. Membership here makes a stage *registered* —
// its code exists and ValidStage(name) is true — independent of whether it is in
// the active chain. M1 registers ingest and transcribe; diarize/moments/render
// append as they land. Which of these actually run, and in what order, is the
// config-driven active chain (see defaultActiveStages / (*Runner).SetActiveStages).
var stageRegistry = []stageDef{
	{name: StageIngest, run: (*Runner).runIngest},
	{name: StageTranscribe, run: (*Runner).runTranscribe},
	{name: StageDiarize, run: (*Runner).runDiarize},
}

// defaultActiveStages is the active chain when PIPELINE_STAGES is unset: ingest
// only, which makes ingest terminal (its success flips the episode to 'ready').
// Transcribe stays registered but out of the chain until PIPELINE_STAGES names
// it — the reversible gate that keeps a prod worker without ASR config, and the
// offline demo/e2e upload->ready flow, ingest-terminal without deleting the
// transcribe stage.
var defaultActiveStages = []Stage{StageIngest}

// defaultActiveDefs is the resolved default active chain, used by a Runner whose
// active chain was not set explicitly (via SetActiveStages) — tests, and any
// wiring that leaves PIPELINE_STAGES at its default.
var defaultActiveDefs = mustResolveActiveStages(defaultActiveStages)

// lookupRegistered finds a registered stage by name in the registry.
func lookupRegistered(name Stage) (stageDef, bool) {
	for _, st := range stageRegistry {
		if st.name == name {
			return st, true
		}
	}
	return stageDef{}, false
}

// ValidStage reports whether s names a registered stage the worker knows how to
// run (ingest or transcribe in M1) — regardless of whether it is in the active
// chain. cmd/worker validates its stage argument against this, so a
// not-yet-implemented stage name is rejected up front while a
// registered-but-inactive stage stays a legal argument.
func ValidStage(s string) bool {
	_, ok := lookupRegistered(Stage(s))
	return ok
}

// resolveActiveStages validates an ordered active-stage chain (from
// PIPELINE_STAGES) against the registry and returns the resolved, ordered defs.
// An empty list yields the default ingest-only chain; a non-empty list must
// start with ingest and name only registered, non-duplicate stages, or it
// returns an error so cmd/worker can fail fast at startup rather than stall an
// episode mid-pipeline later.
func resolveActiveStages(names []Stage) ([]stageDef, error) {
	if len(names) == 0 {
		names = defaultActiveStages
	}
	if names[0] != StageIngest {
		return nil, fmt.Errorf("pipeline: active stage chain must start with %q, got %q", StageIngest, names[0])
	}
	defs := make([]stageDef, 0, len(names))
	seen := make(map[Stage]bool, len(names))
	for _, n := range names {
		if seen[n] {
			return nil, fmt.Errorf("pipeline: duplicate stage %q in active chain", n)
		}
		seen[n] = true
		def, ok := lookupRegistered(n)
		if !ok {
			return nil, fmt.Errorf("pipeline: unknown stage %q in active chain", n)
		}
		defs = append(defs, def)
	}
	return defs, nil
}

// mustResolveActiveStages resolves a known-good default chain at package init and
// panics on a misconfiguration (a programming error, caught immediately).
func mustResolveActiveStages(names []Stage) []stageDef {
	defs, err := resolveActiveStages(names)
	if err != nil {
		panic(err)
	}
	return defs
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
	// Diarizer groups an episode's transcript into speaker turns via the LLM seam
	// (internal/diarize, behind internal/llm). Only the diarize stage consults it;
	// nil for a worker whose active chain excludes diarize (the default). Provider
	// choice never crosses this seam.
	Diarizer Diarizer
	// Speakers reads the segments to diarize (with the audit scope) and persists the
	// resulting speaker_key per segment (idempotent, org-scoped). Only the diarize
	// stage consults it; nil when diarize is not in the active chain.
	Speakers SpeakerStore
	// stages is the runner's active stage chain (ordered). Nil falls back to the
	// default ingest-only chain (defaultActiveDefs). cmd/worker installs the
	// config-driven chain via SetActiveStages (PIPELINE_STAGES); tests either call
	// SetActiveStages or inject a fake chain here directly to exercise auto-advance
	// without a real downstream stage.
	stages []stageDef
}

// registry returns the runner's effective, ordered active stage chain: the
// injected/configured override, else the package default (ingest-only).
func (r *Runner) registry() []stageDef {
	if r.stages != nil {
		return r.stages
	}
	return defaultActiveDefs
}

// SetActiveStages installs the runner's ordered active stage chain from a list
// of stage names (PIPELINE_STAGES, empty = the default ingest-only chain). It
// validates the list against the registry — every name must be registered, the
// chain must start with ingest, and no stage may repeat — and returns an error
// so cmd/worker fails fast at startup on a bad list. The resolved chain drives
// auto-advance, the continuation-claim predecessor guard, and terminal detection
// exactly as the default does.
func (r *Runner) SetActiveStages(names []Stage) error {
	defs, err := resolveActiveStages(names)
	if err != nil {
		return err
	}
	r.stages = defs
	return nil
}

// HasStage reports whether stage s is part of the runner's active chain (after
// SetActiveStages, else the default ingest-only chain). cmd/worker uses it to
// build only the dependencies the active chain needs — the speech engine and
// segment store are constructed only for a chain that includes transcribe, so an
// ingest-only worker needs no ASR configuration.
func (r *Runner) HasStage(s Stage) bool {
	_, _, ok := r.lookupStage(s)
	return ok
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
// stage is terminal (the last stage of the active chain — ingest under the
// default ingest-only chain), it flips the episode to 'ready' via MarkReady,
// exactly the M0 single-stage behaviour. If a next stage is in the active chain
// it is a non-terminal stage: AdvanceStage records the
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
