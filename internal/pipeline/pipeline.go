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

	"blueshift/internal/asr"
	"blueshift/internal/media"
)

// Stage names a unit of pipeline work. The M1 pipeline is ingest -> transcribe
// -> diarize -> moments -> render; these constants name the whole set (they match
// the episodes.current_stage CHECK and the Library's five bars). A stage is
// *runnable* once it is in the stage registry (stageRegistry) — ingest,
// transcribe, diarize, and moments are registered; render lands with its own
// task. Which registered stages
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
	// StageMoments proposes the episode's ranked clip-worthy moments as segment
	// spans via the LLM seam (internal/moments + internal/llm). Registered
	// (runnable) but, like transcribe and diarize, PARKED — it joins the active
	// chain only when PIPELINE_STAGES names it; under the default ingest-only
	// chain it stays out of the chain with its code and tests intact.
	StageMoments Stage = "moments"
	// StageRender names the remaining downstream stage. It is declared here (and
	// allowed by the DB CHECK) but is not yet in the stage registry, so the
	// worker refuses to run it until its implementation lands.
	StageRender Stage = "render"
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
// or intermediate (AdvanceStage). Prov carries the run's provenance facts for
// the stage_runs record (best-effort observability, never load-bearing).
type stageOutput struct {
	ProxyKey   string
	DurationMs int64
	Prov       StageRunFacts
}

// StageRunFacts is what one stage run learned about itself, recorded on the
// stage_runs provenance row at finalize. Zero values mean "unknown" and are
// persisted as NULL — provenance never invents a number. It is observability
// data only: nothing in the status machine or the cost-safety guards reads it.
type StageRunFacts struct {
	// ItemsIn/ItemsOut are stage-meaningful unit counts (e.g. transcribe:
	// provider segments in -> resegmented segments out; moments: segments in ->
	// proposals out). 0 = unknown.
	ItemsIn  int
	ItemsOut int
	// Attempt is the billable counter (episodes.process_attempts) at the stage's
	// paid engine call — the value BeginBillableAttempt returned. 0 = the run
	// made no billable call (ingest, or an idempotency skip).
	Attempt int
	// CostCents is an explicitly computed cost in integer cents (the ASR
	// duration-rate). 0 = not computed here; the store may still link a cost
	// from the llm_calls audit for LLM stages.
	CostCents int
	// Params records the tunables the run used (e.g. segmentation thresholds)
	// as a JSON object, only where tunables exist. Nil = none.
	Params []byte
}

// StageRunFinish is the finalize half of the provenance record: the outcome
// plus the run's facts. Outcome is 'ok' or 'failed' (the stage_runs CHECK).
type StageRunFinish struct {
	Outcome string
	Facts   StageRunFacts
}

// Stage-run outcome values (stage_runs.outcome CHECK).
const (
	RunOutcomeOK     = "ok"
	RunOutcomeFailed = "failed"
)

// RunRecorder is the stage-run provenance seam (stage_runs). StartStageRun
// opens a history row at claim time (append-only: a re-run inserts a new row);
// FinishStageRun closes it at finalize. Both are org-scoped like every other
// finalizer, and both are BEST-EFFORT from the runner's point of view: a
// provenance miss is logged and swallowed, it never fails a run, never touches
// the status machine, and never rides inside the claim's compare-and-set.
// runID 0 from Start means "no row opened" and Finish must no-op on it.
type RunRecorder interface {
	StartStageRun(ctx context.Context, orgID, episodePublicID, stage, engineLabel, engineDetail string) (runID int64, err error)
	FinishStageRun(ctx context.Context, runID int64, fin StageRunFinish) error
}

// StageEngine names the engine identity a stage runs under, for the provenance
// record only. Label is the PUBLIC versioned neutral label (bs-media-1,
// bs-asr-2, bs-lm-1); Detail is the PRIVATE provider truth (model@location) —
// it goes to the DB/server only and never crosses into a client surface.
type StageEngine struct {
	Label  string
	Detail string
}

// stageRegistry is the full set of runnable stages the worker knows how to
// execute, in declaration order. Membership here makes a stage *registered* —
// its code exists and ValidStage(name) is true — independent of whether it is in
// the active chain. M1 registers ingest and transcribe; diarize/moments/render
// append as they land. Which of these actually run, and in what order, is the
// config-driven active chain (see defaultActiveStages / (*Runner).SetActiveStages).
//
// COST-SAFETY KILL SWITCH (CLAUDE.md "Billable-service cost safety", item 4):
// transcribe, diarize, and moments are the only BILLABLE stages here (the
// metered ASR / LLM engines); all are registered but PARKED — out of the default
// active chain. PIPELINE_STAGES is
// the operator escape hatch: setting it to `ingest` (or unsetting it — the default)
// removes every billable stage from the active chain, so a worker makes ZERO paid
// engine calls, effective on the next execution with no code change and no deploy
// (the gate landed in m1-stages-config-gate; SetActiveStages / resolveActiveStages
// enforce it). The other guards below the seam (idempotency skip + process_attempts
// cap) bound cost while a billable stage IS active; this bounds it to nothing.
var stageRegistry = []stageDef{
	{name: StageIngest, run: (*Runner).runIngest},
	{name: StageTranscribe, run: (*Runner).runTranscribe},
	{name: StageDiarize, run: (*Runner).runDiarize},
	{name: StageMoments, run: (*Runner).runMoments},
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
// run (ingest, transcribe, diarize, moments) — regardless of whether it is in
// the active chain. cmd/worker validates its stage argument against this, so a
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
	// BeginBillableAttempt is the per-episode cost-safety gate a billable stage
	// (transcribe, diarize) calls immediately before it would start a paid engine
	// call. It atomically increments the episode's process_attempts and returns the
	// new count with allowed=true ONLY while the pre-increment count was below
	// maxAttempts; at/above the cap it changes nothing and returns allowed=false, so
	// the stage refuses to bill and hard-fails. Org-scoped like the other finalizers;
	// an unknown/foreign org also yields allowed=false (the fail-safe direction). It
	// is the absolute ceiling on how many paid calls an episode can ever trigger — the
	// idempotency guards stop re-billing on SUCCESS, this stops it on repeated FAILURE.
	BeginBillableAttempt(ctx context.Context, orgID, episodePublicID string, maxAttempts int) (attempt int, allowed bool, err error)
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
	// MaxProcessAttempts is the per-episode ceiling on how many times a BILLABLE
	// stage (transcribe/diarize) may start a paid engine call before it hard-fails
	// without calling the engine (CLAUDE.md "Billable-service cost safety", item 3).
	// It maps to MAX_PROCESS_ATTEMPTS; <=0 uses DefaultMaxProcessAttempts. It bounds
	// runaway cost from an unforeseen re-drive/retry loop even if every other guard is
	// bypassed. cmd/worker sets it from config.
	MaxProcessAttempts int
	// Reprocess forces the billable stages to IGNORE their idempotency skip and
	// re-run the paid engine even when the output already exists (segments /
	// speaker_keys) — a deliberate operator re-process, mapped to PIPELINE_REPROCESS
	// (default false). A plain retry/re-drive leaves it false, so it never re-bills an
	// already-completed stage; only an explicit reprocess execution sets it. It does
	// NOT bypass the attempt cap (MaxProcessAttempts still applies).
	Reprocess bool
	// SegmentGapMs / SegmentMaxDurationMs / SegmentMaxWords tune the transcribe
	// stage's deterministic pause-based resegmentation (asr.Resegment): the
	// inter-word silence treated as a turn boundary, and the duration/word caps
	// per produced segment. They map to SEGMENT_GAP_MS / SEGMENT_MAX_DURATION_MS
	// / SEGMENT_MAX_WORDS; any value <= 0 defers to the code defaults in
	// internal/asr (700ms / 30s / 60 words — the single home of those defaults).
	SegmentGapMs         int
	SegmentMaxDurationMs int
	SegmentMaxWords      int
	// ASRPriceCentsPerHour is the speech engine's duration-rate in integer cents
	// per hour of audio (money in integer cents; a price is config data, never a
	// code constant). It maps to ASR_PRICE_CENTS_PER_HOUR and is used ONLY to
	// cost the stage_runs provenance row for a transcribe run that actually
	// called the engine. <=0 (unset) records no cost (NULL) — provenance never
	// invents a number. It has no effect on any cost-safety guard.
	ASRPriceCentsPerHour int
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
	// DefaultMaxProcessAttempts is the per-episode billable-attempt ceiling when
	// Config.MaxProcessAttempts is unset (CLAUDE.md "Billable-service cost safety").
	// Every paid engine call a billable stage starts increments the counter; at this
	// many it refuses to call the engine. It is the absolute per-episode cost bound,
	// independent of the per-run retry budget (DefaultRetries). Mirrors
	// config.defaultMaxProcessAttempts; production flows the value from
	// MAX_PROCESS_ATTEMPTS via cmd/worker.
	DefaultMaxProcessAttempts = 5
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

func (c Config) maxProcessAttempts() int {
	if c.MaxProcessAttempts <= 0 {
		return DefaultMaxProcessAttempts
	}
	return c.MaxProcessAttempts
}

// resegmentOptions packages the segmentation knobs for asr.Resegment. Fields
// left <= 0 stay zero here; asr.Resegment resolves them to its own documented
// defaults, keeping internal/asr the single home of the default thresholds.
func (c Config) resegmentOptions() asr.ResegmentOptions {
	return asr.ResegmentOptions{
		GapMs:         c.SegmentGapMs,
		MaxDurationMs: c.SegmentMaxDurationMs,
		MaxWords:      c.SegmentMaxWords,
	}
}

// ErrStageFailed reports that a stage exhausted its attempts. The episode is
// already recorded 'failed' with a neutral error_id by the time it surfaces;
// cmd/worker maps it to exit code 1 for Cloud Run Jobs semantics.
var ErrStageFailed = errors.New("pipeline: stage failed after retries")

// ErrBillableCapReached reports that a billable stage refused to call its paid
// engine because the episode hit the per-episode attempt ceiling
// (Config.MaxProcessAttempts / MAX_PROCESS_ATTEMPTS — CLAUDE.md "Billable-service
// cost safety"). The stage returns it INSTEAD of ever touching the engine; the Run
// loop treats it as a terminal, non-retryable failure (retrying cannot help and
// must not bill), marks the episode 'failed' with a neutral error_id, and the
// message names no provider. It never reaches a client surface.
var ErrBillableCapReached = errors.New("pipeline: per-episode billable attempt cap reached")

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
	// Selector proposes an episode's ranked moments via the LLM seam
	// (internal/moments, behind internal/llm). Only the moments stage consults
	// it; nil for a worker whose active chain excludes moments (the default).
	// Provider choice never crosses this seam.
	Selector MomentSelector
	// Moments reads the speaker-aware transcript (with the audit scope) and
	// persists the proposed moment set (idempotent, org-scoped). Only the moments
	// stage consults it; nil when moments is not in the active chain.
	Moments MomentStore
	// Runs records stage-run provenance (stage_runs): a history row opened at
	// claim and closed at finalize. Nil disables recording (tests, or a wiring
	// that opts out) — provenance is best-effort observability and never
	// load-bearing, so every call through this seam logs-and-swallows errors.
	Runs RunRecorder
	// Engines names the engine identity each stage runs under, for the
	// provenance record only (public label + private detail). Nil or a missing
	// stage records NULL engine fields. cmd/worker builds it from config.
	Engines map[Stage]StageEngine
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

	// Open the stage-run provenance row (history: a re-run inserts a new row;
	// latest-per-stage wins for display). Best-effort by contract: a miss is
	// logged and the run proceeds with runID 0 (Finish no-ops on it) — the
	// provenance record never gates the status machine.
	runID := r.startStageRun(ctx, ep, stage)

	attempts := 1 + r.Config.retries()
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		out, aerr := def.run(r, ctx, ep, attempt)
		if aerr == nil {
			return r.finalizeSuccess(ctx, ep, stage, idx, runID, out, attempt)
		}
		lastErr = aerr
		// The raw cause (which may name codecs/tools) stays in server logs only.
		log.WarnContext(ctx, "stage attempt failed",
			slog.String("episode", ep.PublicID), slog.String("stage", stage),
			slog.Int("attempt", attempt), slog.Int("attempts", attempts),
			slog.String("error", aerr.Error()))
		// A billable-attempt-cap refusal is terminal: no billable call was made and
		// retrying can only re-hit the cap (still no call). Stop immediately rather
		// than burning the remaining attempts on a decision that cannot change — the
		// episode is then marked 'failed' below with a neutral error_id.
		if errors.Is(aerr, ErrBillableCapReached) {
			break
		}
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
	// Close the provenance row as failed — AFTER the load-bearing mark-failed,
	// on the SAME context: in the shutdown case that is the detached, bounded
	// finalize context, so this best-effort write shares (never stretches) the
	// existing grace-window budget and is simply cut off with it if time runs out.
	r.finishStageRun(finCtx, ep, stage, runID, StageRunFinish{Outcome: RunOutcomeFailed})
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
func (r *Runner) finalizeSuccess(ctx context.Context, ep Episode, stage string, idx int, runID int64, out stageOutput, attempt int) error {
	log := r.logger()
	next, hasNext := r.nextStageName(idx)
	if !hasNext {
		// Terminal stage: record outputs and mark ready.
		if err := r.Repo.MarkReady(ctx, ep.OrgID, ep.PublicID, out.ProxyKey, out.DurationMs); err != nil {
			return fmt.Errorf("pipeline: mark ready: %w", err)
		}
		r.finishStageRun(ctx, ep, stage, runID, StageRunFinish{Outcome: RunOutcomeOK, Facts: out.Prov})
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
	// Provenance close AFTER the load-bearing handoff and BEFORE the trigger, so
	// the completed run's timing is durable before the next stage can start.
	r.finishStageRun(ctx, ep, stage, runID, StageRunFinish{Outcome: RunOutcomeOK, Facts: out.Prov})
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

// startStageRun opens the stage-run provenance row for a just-claimed stage and
// returns its id (0 when no recorder is wired, the row could not be opened, or
// the episode is invisible to the recorder — Finish no-ops on 0). Best-effort:
// an error is logged at WARN and swallowed; provenance never fails a run and
// sits outside the claim's compare-and-set, so claim atomicity is untouched.
func (r *Runner) startStageRun(ctx context.Context, ep Episode, stage string) int64 {
	if r.Runs == nil {
		return 0
	}
	eng := r.Engines[Stage(stage)]
	runID, err := r.Runs.StartStageRun(ctx, ep.OrgID, ep.PublicID, stage, eng.Label, eng.Detail)
	if err != nil {
		r.logger().WarnContext(ctx, "stage run provenance open failed",
			slog.String("episode", ep.PublicID), slog.String("stage", stage),
			slog.String("error", err.Error()))
		return 0
	}
	return runID
}

// finishStageRun closes the stage-run provenance row (outcome + facts).
// Best-effort like startStageRun: a miss is logged at WARN and swallowed. On
// the shutdown path the caller passes the detached, bounded finalize context,
// so this write shares the existing grace-window budget and never stretches it.
func (r *Runner) finishStageRun(ctx context.Context, ep Episode, stage string, runID int64, fin StageRunFinish) {
	if r.Runs == nil || runID == 0 {
		return
	}
	if err := r.Runs.FinishStageRun(ctx, runID, fin); err != nil {
		r.logger().WarnContext(ctx, "stage run provenance close failed",
			slog.String("episode", ep.PublicID), slog.String("stage", stage),
			slog.String("error", err.Error()))
	}
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
