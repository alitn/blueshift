package pipeline

// diarize.go is the third registered stage: it assigns an episode-local speaker
// label to every transcript segment by asking the LLM to group segments into
// speaker turns, anchored to the ASR TEXT (never timestamps, never by rewriting
// text). Like transcribe, it is REGISTERED (runnable, a valid stage argument) but
// PARKED — it is not in the default active chain, so it runs only when
// PIPELINE_STAGES names it. The default worker stays ingest-terminal, unchanged.
//
// Verbatim invariant (CLAUDE.md — "LLMs decide, they never measure"): this stage
// writes ONLY the speaker_key column. Segment text, words, and every *_ms timing
// are untouched; the LLM decides grouping and nothing else. The provider-specific
// work (schema-constrained generation, one-retry-then-fail, the llm_calls audit)
// lives entirely behind the internal/diarize + internal/llm seams — no provider
// name ever crosses into this package. The stage only orchestrates the org-scoped
// read, the neutral diarizer call, and the org-scoped persist.
//
// Why the LLM call is NOT in this package: store imports pipeline (it implements
// pipeline.Repo), and internal/llm imports store (its audit adapter), so pipeline
// importing internal/llm would form an import cycle. The diarizer that actually
// talks to the LLM therefore lives in internal/diarize and is injected here
// through the neutral Diarizer seam — mirroring how the ASR engine is injected
// through the ASR seam.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"blueshift/internal/asr"
)

// Diarizer groups an episode's transcript segments into speaker turns and returns
// an idx -> speaker_key assignment (e.g. {0:"S1", 1:"S1", 2:"S2"}). It is the
// neutral seam the diarize stage drives; the concrete implementation
// (internal/diarize.Engine) builds the text-anchored LLM request, validates the
// output against the segment set, and audits the call. orgID/episodeID are the
// INTERNAL ids the llm_calls audit row is scoped by (resolved by the stage via
// the SpeakerStore). Provider choice never crosses this seam — the stage gets a
// map and neutral llm sentinel errors only.
type Diarizer interface {
	Diarize(ctx context.Context, language string, orgID, episodeID int64, segs []asr.Segment) (map[int]string, error)
}

// SegmentSet is an episode's transcript plus the internal org/episode ids the
// diarize stage needs to scope the llm_calls audit. It is what SpeakerStore hands
// the stage: the segments to group (idx-ordered) and the audit scope, resolved in
// one org-scoped read.
type SegmentSet struct {
	// OrgID and EpisodeID are the INTERNAL (bigint) ids, used only to scope the
	// audit row — never exposed to a client surface.
	OrgID     int64
	EpisodeID int64
	// Segments is the episode's transcript in idx order.
	Segments []asr.Segment
}

// SpeakerStore is the diarize stage's view of segment persistence. All methods
// are org-scoped, so a re-driven stage can never read or write across tenants.
type SpeakerStore interface {
	// SegmentsForDiarize returns the episode's idx-ordered transcript together with
	// the internal ids the audit is scoped by. found=false for an unknown/foreign
	// episode (a clean skip, not an error), matching the store's other finalizers.
	SegmentsForDiarize(ctx context.Context, orgID, episodePublicID string) (SegmentSet, bool, error)
	// SetSegmentSpeakers writes the idx -> speaker_key grouping for the episode in
	// one transaction, idempotently: a re-run overwrites the prior grouping rather
	// than duplicating it. It writes ONLY speaker_key (verbatim invariant).
	SetSegmentSpeakers(ctx context.Context, orgID, episodePublicID string, byIdx map[int]string) error
	// SpeakersAssigned reports whether the episode is already fully diarized (it has
	// segments and every one carries a speaker_key), org-scoped. A true result means
	// the diarize stage must SKIP the billable LLM call — never re-billing on a
	// retry/re-drive (CLAUDE.md "Billable-service cost safety").
	SpeakersAssigned(ctx context.Context, orgID, episodePublicID string) (bool, error)
}

// runDiarize adapts the diarize stage to the registry's run signature. It reads
// the episode's segments (idx-ordered, org-scoped), asks the injected Diarizer to
// group them into speaker turns, and persists the resulting speaker_key per
// segment in one transaction — all under a per-attempt timeout so a wedged engine
// is retried. It produces no proxy/duration outputs of its own: the terminal
// finalize preserves the ones ingest recorded (MarkEpisodeReady COALESCEs a NULL
// arg, exactly as transcribe relies on).
//
// COST SAFETY (CLAUDE.md "Billable-service cost safety"). The LLM is the billable
// engine, so two guards bound its cost, mirroring transcribe:
//   - Idempotency: if the episode is already fully diarized (every segment carries a
//     speaker_key), the paid call was already made — SKIP it. A plain retry/re-drive
//     never re-bills; only Config.Reprocess forces a fresh grouping.
//   - Attempt cap: BeginBillableAttempt increments process_attempts and refuses the
//     call once the per-episode ceiling is reached.
//
// Max billable calls per episode: one attempt makes exactly ONE Diarizer call,
// which internal/llm bounds to at most maxAttempts=2 provider calls (one initial +
// one retry on invalid output — see internal/llm). The per-run retry loop and every
// re-drive each increment process_attempts and are refused at the cap, so diarize
// can ever trigger at most Config.maxProcessAttempts billable *attempts* (≤ 2× that
// many provider calls), sharing the counter with transcribe.
func (r *Runner) runDiarize(parent context.Context, ep Episode, _ int) (stageOutput, error) {
	ctx, cancel := context.WithTimeout(parent, r.Config.stageTimeout())
	defer cancel()

	if r.Diarizer == nil {
		return stageOutput{}, errors.New("diarize: no diarizer seam configured")
	}
	if r.Speakers == nil {
		return stageOutput{}, errors.New("diarize: no speaker store configured")
	}

	// Idempotency guard: skip the billable LLM call when the episode is already
	// diarized. First, so a re-drive of an already-diarized episode is a free no-op.
	if !r.Config.Reprocess {
		done, err := r.Speakers.SpeakersAssigned(ctx, ep.OrgID, ep.PublicID)
		if err != nil {
			return stageOutput{}, fmt.Errorf("diarize: check existing speakers: %w", err)
		}
		if done {
			r.logger().InfoContext(ctx, "already diarized; skipping",
				slog.String("episode", ep.PublicID), slog.String("stage", string(StageDiarize)))
			return stageOutput{}, nil
		}
	}

	set, ok, err := r.Speakers.SegmentsForDiarize(ctx, ep.OrgID, ep.PublicID)
	if err != nil {
		return stageOutput{}, fmt.Errorf("diarize: read segments: %w", err)
	}
	if !ok {
		// The stage runs only after transcribe persisted segments; a missing episode
		// is an out-of-order run, not something to guess at.
		return stageOutput{}, errors.New("diarize: episode has no segments to diarize")
	}
	if len(set.Segments) == 0 {
		return stageOutput{}, errors.New("diarize: episode has no segments to diarize")
	}

	// Attempt cap: the segment read above is non-billable prep (an out-of-order run
	// fails without consuming budget). Here — immediately before the paid Diarizer
	// call — record the billable attempt and refuse it at the per-episode ceiling, so
	// a capped episode bills NOTHING.
	billAttempt, allowed, err := r.Repo.BeginBillableAttempt(ctx, ep.OrgID, ep.PublicID, r.Config.maxProcessAttempts())
	if err != nil {
		return stageOutput{}, fmt.Errorf("diarize: begin billable attempt: %w", err)
	}
	if !allowed {
		r.logger().ErrorContext(ctx, "diarize blocked: per-episode billable attempt cap reached",
			slog.String("episode", ep.PublicID), slog.Int("max_process_attempts", r.Config.maxProcessAttempts()))
		return stageOutput{}, fmt.Errorf("%w (stage=diarize max=%d)", ErrBillableCapReached, r.Config.maxProcessAttempts())
	}
	r.logger().InfoContext(ctx, "billable diarize attempt",
		slog.String("episode", ep.PublicID), slog.Int("process_attempts", billAttempt),
		slog.Int("max_process_attempts", r.Config.maxProcessAttempts()))

	// The Diarizer sends the LLM only {idx, text} per segment — no timestamps — and
	// returns an idx -> speaker_key map validated to cover every segment exactly
	// once. A neutral llm sentinel (invalid output after one retry, engine
	// unavailable) surfaces here and is treated as a stage failure: the run's retry
	// loop re-attempts, and on exhaustion the episode is marked failed with a
	// neutral error_id (no provider text ever leaks).
	byIdx, err := r.Diarizer.Diarize(ctx, ep.Language, set.OrgID, set.EpisodeID, set.Segments)
	if err != nil {
		return stageOutput{}, fmt.Errorf("diarize: group speakers: %w", err)
	}

	if err := r.Speakers.SetSegmentSpeakers(ctx, ep.OrgID, ep.PublicID, byIdx); err != nil {
		return stageOutput{}, fmt.Errorf("diarize: persist speakers: %w", err)
	}

	r.logger().InfoContext(ctx, "diarize complete",
		slog.String("episode", ep.PublicID),
		slog.Int("segments", len(set.Segments)),
		slog.Int("speakers", distinctSpeakers(byIdx)))

	// No outputs to record: a terminal diarize marks ready preserving ingest's
	// proxy key + measured duration (MarkEpisodeReady COALESCEs a NULL arg).
	return stageOutput{}, nil
}

// distinctSpeakers counts the distinct speaker_key values in an assignment, for
// the completion log line only.
func distinctSpeakers(byIdx map[int]string) int {
	seen := make(map[string]struct{}, len(byIdx))
	for _, k := range byIdx {
		seen[k] = struct{}{}
	}
	return len(seen)
}
