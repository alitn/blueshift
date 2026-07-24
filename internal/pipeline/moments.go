package pipeline

// moments.go is the fourth registered stage: it asks the LLM to propose the
// episode's 3..8 most clip-worthy moments as RANKED SEGMENT SPANS, anchored to
// the transcript. Like transcribe and diarize it is REGISTERED (runnable, a
// valid stage argument) but joins the active chain only when PIPELINE_STAGES
// names it; the default worker stays ingest-terminal, unchanged.
//
// Verbatim invariant (CLAUDE.md — "LLMs decide, they never measure"): the model
// returns segment-idx spans, an English rationale, and a quote that MUST be a
// verbatim contiguous substring of the span's transcript text (validated behind
// the engine seam). It never emits a timestamp that is believed: the persisted
// start_ms/end_ms are derived HERE, WORD-ACCURATELY, by locating the quote in
// the span's word sequence (asr.LocateQuote — the same joinWords rule as
// resegmentation) and reading the quote's first word's start_ms and last
// word's end_ms from the stored ASR word data. The model only quotes text; the
// stage looks the times up. Segments themselves are never touched by this stage.
//
// Why the LLM call is NOT in this package: the same import-cycle reason as
// diarize (store implements pipeline.Repo, internal/llm audits through store),
// so the selector that talks to the LLM lives in internal/moments and is
// injected here through the neutral MomentSelector seam. The seam's value types
// live on THIS side (pipeline) because the selector returns structs, not a
// builtin map — internal/moments imports pipeline for them; pipeline never
// imports internal/moments, so the dependency arrow still points the safe way.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"blueshift/internal/asr"
)

// MomentSegment is one transcript segment as the moments stage reads it: the
// verbatim ASR segment plus its diarization speaker_key ("" when not yet
// diarized — the selector treats the key as optional context, never a
// requirement). JSON tags exist so eval fixtures can serialize the exact shape.
type MomentSegment struct {
	asr.Segment
	SpeakerKey string `json:"speaker_key,omitempty"`
}

// MomentSegmentSet is an episode's speaker-aware transcript plus the internal
// org/episode ids the moments stage needs to scope the llm_calls audit — what
// MomentStore hands the stage in one org-scoped read, mirroring SegmentSet.
type MomentSegmentSet struct {
	// OrgID and EpisodeID are the INTERNAL (bigint) ids, used only to scope the
	// audit row — never exposed to a client surface.
	OrgID     int64
	EpisodeID int64
	// Segments is the episode's transcript in idx order.
	Segments []MomentSegment
}

// ProposedMoment is one ranked moment the selector proposes: an inclusive
// segment-idx span, the best-first rank, an English rationale, and the quote
// copied verbatim from the span's transcript text (the engine has already
// validated the substring property). It carries NO times — the model never
// emits a timestamp that survives.
type ProposedMoment struct {
	Rank        int    `json:"rank"`
	StartIdx    int    `json:"start_idx"`
	EndIdx      int    `json:"end_idx"`
	RationaleEn string `json:"rationale_en"`
	QuoteFa     string `json:"quote_fa"`
}

// MomentRow is a persisted moment: the proposal plus the quote-derived ASR
// times the stage computes before persisting — the quote's FIRST word's
// start_ms and LAST word's end_ms, located word-accurately within the span
// (asr.LocateQuote). Times here are measured data, never model output. JSON
// tags exist so the eval golden can pin the derived values byte-stable.
type MomentRow struct {
	ProposedMoment
	StartMs int `json:"start_ms"`
	EndMs   int `json:"end_ms"`
}

// MomentSelector proposes an episode's ranked moments from its speaker-aware
// transcript. It is the neutral seam the moments stage drives; the concrete
// implementation (internal/moments.Engine) builds the LLM request, validates
// the spans/ranks/verbatim quotes against the segment set, and audits the call.
// orgID/episodeID are the INTERNAL ids the llm_calls audit row is scoped by.
// Provider choice never crosses this seam — the stage gets proposals and
// neutral llm sentinel errors only.
type MomentSelector interface {
	SelectMoments(ctx context.Context, language string, orgID, episodeID int64, segs []MomentSegment) ([]ProposedMoment, error)
}

// MomentStore is the moments stage's view of persistence. All methods are
// org-scoped, so a re-driven stage can never read or write across tenants.
type MomentStore interface {
	// SegmentsForMoments returns the episode's idx-ordered, speaker-aware
	// transcript together with the internal ids the audit is scoped by.
	// found=false for an unknown/foreign episode (a clean skip, not an error),
	// matching the store's other finalizers.
	SegmentsForMoments(ctx context.Context, orgID, episodePublicID string) (MomentSegmentSet, bool, error)
	// ReplaceMoments persists the episode's proposed moments in one transaction,
	// idempotently: a re-run replaces the prior proposal set wholesale rather
	// than duplicating it (mirroring ReplaceSegments).
	ReplaceMoments(ctx context.Context, orgID, episodePublicID string, rows []MomentRow) error
	// MomentsExist reports whether the episode already has persisted moments,
	// org-scoped. A true result means the moments stage must SKIP the billable
	// LLM call — never re-billing on a retry/re-drive (CLAUDE.md
	// "Billable-service cost safety").
	MomentsExist(ctx context.Context, orgID, episodePublicID string) (bool, error)
}

// runMoments adapts the moments stage to the registry's run signature. It reads
// the episode's speaker-aware transcript (idx-ordered, org-scoped), asks the
// injected MomentSelector for the ranked proposals, derives each quote's
// word-accurate ASR times, and persists the moment set in one transaction — all under a
// per-attempt timeout so a wedged engine is retried. It produces no
// proxy/duration outputs of its own: the terminal finalize preserves the ones
// ingest recorded (MarkEpisodeReady COALESCEs a NULL arg, exactly as diarize
// relies on).
//
// COST SAFETY (CLAUDE.md "Billable-service cost safety"). The LLM is the
// billable engine, so the same two guards as transcribe/diarize bound its cost:
//   - Idempotency: if the episode already has moments, the paid call was already
//     made — SKIP it. A plain retry/re-drive never re-bills; only Config.Reprocess
//     forces a fresh proposal.
//   - Attempt cap: BeginBillableAttempt increments process_attempts and refuses
//     the call once the per-episode ceiling is reached.
//
// Max billable calls per episode: one attempt makes exactly ONE SelectMoments
// call, which internal/llm bounds to at most maxAttempts=2 provider calls (one
// initial + one retry on invalid output). Every attempt draws on the SAME
// process_attempts counter transcribe and diarize share, so the whole pipeline
// stays under Config.maxProcessAttempts billable attempts per episode.
func (r *Runner) runMoments(parent context.Context, ep Episode, _ int) (stageOutput, error) {
	ctx, cancel := context.WithTimeout(parent, r.Config.stageTimeout())
	defer cancel()

	if r.Selector == nil {
		return stageOutput{}, errors.New("moments: no moment selector seam configured")
	}
	if r.Moments == nil {
		return stageOutput{}, errors.New("moments: no moment store configured")
	}

	// Idempotency guard: skip the billable LLM call when the episode already has
	// moments. First, so a re-drive of an already-processed episode is a free no-op.
	if !r.Config.Reprocess {
		done, err := r.Moments.MomentsExist(ctx, ep.OrgID, ep.PublicID)
		if err != nil {
			return stageOutput{}, fmt.Errorf("moments: check existing moments: %w", err)
		}
		if done {
			r.logger().InfoContext(ctx, "moments already proposed; skipping",
				slog.String("episode", ep.PublicID), slog.String("stage", string(StageMoments)))
			return stageOutput{}, nil
		}
	}

	set, ok, err := r.Moments.SegmentsForMoments(ctx, ep.OrgID, ep.PublicID)
	if err != nil {
		return stageOutput{}, fmt.Errorf("moments: read segments: %w", err)
	}
	if !ok || len(set.Segments) == 0 {
		// The stage runs only after transcribe persisted segments; a missing
		// transcript is an out-of-order run, not something to guess at.
		return stageOutput{}, errors.New("moments: episode has no segments to select from")
	}

	// Attempt cap: the segment read above is non-billable prep (an out-of-order
	// run fails without consuming budget). Here — immediately before the paid
	// selector call — record the billable attempt and refuse it at the
	// per-episode ceiling, so a capped episode bills NOTHING.
	billAttempt, allowed, err := r.Repo.BeginBillableAttempt(ctx, ep.OrgID, ep.PublicID, r.Config.maxProcessAttempts())
	if err != nil {
		return stageOutput{}, fmt.Errorf("moments: begin billable attempt: %w", err)
	}
	if !allowed {
		r.logger().ErrorContext(ctx, "moments blocked: per-episode billable attempt cap reached",
			slog.String("episode", ep.PublicID), slog.Int("max_process_attempts", r.Config.maxProcessAttempts()))
		return stageOutput{}, fmt.Errorf("%w (stage=moments max=%d)", ErrBillableCapReached, r.Config.maxProcessAttempts())
	}
	r.logger().InfoContext(ctx, "billable moments attempt",
		slog.String("episode", ep.PublicID), slog.Int("process_attempts", billAttempt),
		slog.Int("max_process_attempts", r.Config.maxProcessAttempts()))

	// The selector sends the LLM the idx-ordered transcript and returns validated
	// rank-ordered proposals (contiguous ranks, valid non-overlapping spans,
	// verbatim quotes). A neutral llm sentinel (invalid output after one retry,
	// engine unavailable) surfaces here and is treated as a stage failure: the
	// run's retry loop re-attempts, and on exhaustion the episode is marked failed
	// with a neutral error_id (no provider text ever leaks).
	proposals, err := r.Selector.SelectMoments(ctx, ep.Language, set.OrgID, set.EpisodeID, set.Segments)
	if err != nil {
		return stageOutput{}, fmt.Errorf("moments: select moments: %w", err)
	}

	rows, err := DeriveMomentRows(proposals, set.Segments)
	if err != nil {
		return stageOutput{}, err
	}
	if err := r.Moments.ReplaceMoments(ctx, ep.OrgID, ep.PublicID, rows); err != nil {
		return stageOutput{}, fmt.Errorf("moments: persist moments: %w", err)
	}

	r.logger().InfoContext(ctx, "moments complete",
		slog.String("episode", ep.PublicID),
		slog.Int("segments", len(set.Segments)),
		slog.Int("moments", len(rows)))

	// No outputs to record: a terminal moments stage marks ready preserving
	// ingest's proxy key + measured duration (MarkEpisodeReady COALESCEs a NULL arg).
	return stageOutput{}, nil
}

// DeriveMomentRows derives each proposal's persisted times WORD-ACCURATELY
// from its quote: the quote is located within the span's word sequence
// (asr.LocateQuote — deterministic first-occurrence alignment under the same
// joinWords rule as resegmentation) and the times are the quote's first word's
// start_ms and last word's end_ms, read from the stored ASR word data — never
// model output, and never snapped to segment bounds, so moment precision is
// independent of segment length (verbatim invariant: the model quotes text,
// this function looks the times up). start_idx/end_idx keep the segment span
// for transcript reference. The selector already validated every span and
// aligned every quote against the same segment set, so a failure here — an
// unresolvable idx or a quote that no longer aligns — is a defensive hard
// error, never a guess. Exported so the eval golden derives (and pins) the
// exact rows the stage would persist.
func DeriveMomentRows(proposals []ProposedMoment, segs []MomentSegment) ([]MomentRow, error) {
	byIdx := make(map[int]MomentSegment, len(segs))
	for _, s := range segs {
		byIdx[s.Idx] = s
	}
	rows := make([]MomentRow, 0, len(proposals))
	for _, p := range proposals {
		span := make([]asr.Segment, 0, p.EndIdx-p.StartIdx+1)
		for i := p.StartIdx; i <= p.EndIdx; i++ {
			s, ok := byIdx[i]
			if !ok {
				return nil, fmt.Errorf("moments: proposal rank %d spans unknown segment idx %d..%d", p.Rank, p.StartIdx, p.EndIdx)
			}
			span = append(span, s.Segment)
		}
		startMs, endMs, err := asr.LocateQuote(span, p.QuoteFa)
		if err != nil {
			return nil, fmt.Errorf("moments: proposal rank %d quote does not align to the span's word data: %w", p.Rank, err)
		}
		rows = append(rows, MomentRow{ProposedMoment: p, StartMs: startMs, EndMs: endMs})
	}
	return rows, nil
}
