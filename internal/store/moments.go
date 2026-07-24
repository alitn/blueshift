package store

// moments.go is the moment store: reading the speaker-aware transcript (with
// its audit scope) for the moments stage, persisting the stage's proposed
// moment set, and the human review status flips the moment rail performs. It
// is additive over the segment store (segments.go / diarize.go) — the moments
// table is written here and nothing else is touched.
//
// Verbatim invariant (CLAUDE.md): rationale_en/quote_fa are stored exactly as
// the validated engine returned them (the quote's substring property is
// enforced behind the engine seam before anything reaches this file), and
// start_ms/end_ms arrive stage-derived from the span's segment rows — ASR
// times only. All operations are org-scoped exactly like the segment store,
// so a re-driven stage can never read or write across tenants.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"blueshift/internal/api"
	"blueshift/internal/asr"
	"blueshift/internal/ids"
	"blueshift/internal/pipeline"
	"blueshift/internal/store/db"
)

// The store is the production MomentStore for the moments stage.
var _ pipeline.MomentStore = (*Store)(nil)

// Moment statuses (the moments.status CHECK set). Text + CHECK per the schema
// conventions; 'proposed' is the stage's output, the other two are human
// review verdicts.
const (
	MomentStatusProposed  = "proposed"
	MomentStatusApproved  = "approved"
	MomentStatusDismissed = "dismissed"
)

// SegmentsForMoments returns the episode's speaker-aware transcript
// (idx-ordered) together with the internal org/episode ids the moments stage
// scopes its llm_calls audit by, all org-scoped. found=false (nil error) for
// an unknown/foreign episode — a clean skip, never an error. It implements
// pipeline.MomentStore's read, mirroring SegmentsForDiarize with the additive
// speaker_key ("" when a segment is not yet diarized).
func (s *Store) SegmentsForMoments(ctx context.Context, orgID, episodePublicID string) (pipeline.MomentSegmentSet, bool, error) {
	orgInternal, ep, ok, err := s.resolveEpisodeForSegments(ctx, orgID, episodePublicID)
	if err != nil {
		return pipeline.MomentSegmentSet{}, false, err
	}
	if !ok {
		return pipeline.MomentSegmentSet{}, false, nil
	}
	rows, err := s.EpisodeSegmentsWithSpeakers(ctx, orgID, episodePublicID)
	if err != nil {
		return pipeline.MomentSegmentSet{}, false, err
	}
	segs := make([]pipeline.MomentSegment, 0, len(rows))
	for _, r := range rows {
		segs = append(segs, pipeline.MomentSegment{Segment: r.Segment, SpeakerKey: r.SpeakerKey})
	}
	return pipeline.MomentSegmentSet{OrgID: orgInternal, EpisodeID: ep.ID, Segments: segs}, true, nil
}

// MomentsExist reports whether the episode already has persisted PIPELINE
// (source='auto') moments, org-scoped. It is the moments stage's cost-safety
// idempotency probe (CLAUDE.md "Billable-service cost safety"): a true result
// means the stage's proposal set already exists, so the stage SKIPS the
// billable LLM call entirely and never re-bills on a retry/re-drive.
// ReplaceMoments writes the whole auto set in one transaction, so any auto row
// means a completed stage — there is no partial state to mistake for done.
// Kept user-composed moments (source='prompt') deliberately do NOT count: a
// human keeping a composed moment before the stage ever ran must never
// suppress the stage's own proposal run. An unknown/foreign org or missing
// episode yields false (no error), matching the segment store's contract.
func (s *Store) MomentsExist(ctx context.Context, orgID, episodePublicID string) (bool, error) {
	_, ep, ok, err := s.resolveEpisodeForSegments(ctx, orgID, episodePublicID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	n, err := s.CountAutoMoments(ctx, ep.ID)
	if err != nil {
		return false, fmt.Errorf("store: count moments: %w", err)
	}
	return n > 0, nil
}

// tempRankBase parks kept composed rows on transiently-high ranks inside the
// ReplaceMoments transaction so the fresh auto set can insert at 1..n without
// tripping UNIQUE(episode_id, rank), before the composed rows are renumbered
// to follow the new set. It only ever exists inside that one transaction.
const tempRankBase = 1_000_000

// ReplaceMoments persists the moments stage's proposal set idempotently.
// Within one transaction it deletes the episode's existing PIPELINE
// (source='auto') moments and inserts the new set, so a re-run replaces the
// stage's proposals wholesale rather than duplicating them — the same
// choreography as ReplaceSegments. Kept user-composed moments
// (source='prompt') SURVIVE the replace: a reprocess never discards a human's
// kept composition. Because the fresh auto set always ranks 1..n, surviving
// composed rows are renumbered inside the same transaction to n+1, n+2, …
// (their relative order preserved), so UNIQUE(episode_id, rank) holds no
// matter how the new set's size compares to the old one.
//
// It is org-scoped: the episode is resolved by (org public id, episode public
// id), so a caller can only ever write moments for its own tenant's episode.
// An unknown/foreign org, or an episode that resolves to no row for that org,
// is a clean no-op (nothing to write). Every inserted row starts at the
// 'proposed' status (the column default); a replace therefore also resets any
// prior human verdicts on the AUTO set — a deliberate property of
// reprocessing (kept composed rows keep their verdicts).
func (s *Store) ReplaceMoments(ctx context.Context, orgID, episodePublicID string, rows []pipeline.MomentRow) error {
	_, ep, ok, err := s.resolveEpisodeForSegments(ctx, orgID, episodePublicID)
	if err != nil {
		return err
	}
	if !ok {
		// Unknown/foreign org: nothing to write. A clean no-op.
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin moments tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.WithTx(tx)

	if err := q.DeleteAutoMoments(ctx, ep.ID); err != nil {
		return fmt.Errorf("store: delete moments: %w", err)
	}
	// Park the surviving composed rows on transient high ranks so the fresh
	// auto set can take 1..n; then insert; then renumber the composed rows to
	// follow the new set in their preserved relative order.
	kept, err := q.ListPromptMomentIDs(ctx, ep.ID)
	if err != nil {
		return fmt.Errorf("store: list kept moments: %w", err)
	}
	for i, id := range kept {
		if err := q.SetMomentRank(ctx, db.SetMomentRankParams{ID: id, Rank: int32(tempRankBase + i)}); err != nil {
			return fmt.Errorf("store: park kept moment: %w", err)
		}
	}
	for _, m := range rows {
		if err := q.InsertMoment(ctx, db.InsertMomentParams{
			EpisodeID:   ep.ID,
			Rank:        int32(m.Rank),
			StartIdx:    int32(m.StartIdx),
			EndIdx:      int32(m.EndIdx),
			StartMs:     int32(m.StartMs),
			EndMs:       int32(m.EndMs),
			RationaleEn: m.RationaleEn,
			QuoteFa:     m.QuoteFa,
		}); err != nil {
			return fmt.Errorf("store: insert moment rank %d: %w", m.Rank, err)
		}
	}
	for i, id := range kept {
		if err := q.SetMomentRank(ctx, db.SetMomentRankParams{ID: id, Rank: int32(len(rows) + 1 + i)}); err != nil {
			return fmt.Errorf("store: renumber kept moment: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit moments tx: %w", err)
	}
	return nil
}

// resolveEpisodeForReview resolves (org public id, episode public id) to the
// org-scoped episode row for the human-review surface. Unlike the stage
// methods above (pipeline callers, base32 org_… ids via
// resolveEpisodeForSegments), the review methods serve api.EpisodeRepo, whose
// org id is the session principal's canonical UUID — the same contract as
// GetEpisode/EpisodeTranscript. ok=false (nil error) for a malformed episode
// id or an episode not visible to the org.
func (s *Store) resolveEpisodeForReview(ctx context.Context, orgPublicID, episodePublicID string) (db.Episode, bool, error) {
	org, err := s.resolveOrg(ctx, orgPublicID)
	if err != nil {
		return db.Episode{}, false, err
	}
	epUUID, err := ids.Decode(ids.Episode, episodePublicID)
	if err != nil {
		return db.Episode{}, false, nil
	}
	ep, err := s.GetEpisodeByPublicID(ctx, db.GetEpisodeByPublicIDParams{
		PublicID: pgUUID(epUUID),
		OrgID:    org.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Episode{}, false, nil
	}
	if err != nil {
		return db.Episode{}, false, fmt.Errorf("store: resolve episode for review: %w", err)
	}
	return ep, true, nil
}

// EpisodeMoments returns an episode's moments best-first (rank 1 first),
// org-scoped, projected to the neutral api.EpisodeMoment shape the moment
// handlers serve (mirroring EpisodeTranscript, id contract included: the org
// is the principal's canonical UUID). An unknown/foreign episode yields nil
// (no error), matching the transcript read.
func (s *Store) EpisodeMoments(ctx context.Context, orgPublicID, episodePublicID string) ([]api.EpisodeMoment, error) {
	ep, ok, err := s.resolveEpisodeForReview(ctx, orgPublicID, episodePublicID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	rows, err := s.ListMomentsByEpisode(ctx, ep.ID)
	if err != nil {
		return nil, fmt.Errorf("store: list moments: %w", err)
	}
	out := make([]api.EpisodeMoment, 0, len(rows))
	for _, r := range rows {
		out = append(out, api.EpisodeMoment{
			Rank:            int(r.Rank),
			StartIdx:        int(r.StartIdx),
			EndIdx:          int(r.EndIdx),
			StartMs:         int(r.StartMs),
			EndMs:           int(r.EndMs),
			RationaleEn:     r.RationaleEn,
			QuoteFa:         r.QuoteFa,
			Status:          r.Status,
			StatusChangedAt: r.StatusChangedAt.Time, // zero time when NULL
		})
	}
	return out, nil
}

// SetMomentStatus flips one moment's review status, org-scoped via the review
// resolve (canonical org UUID, like its EpisodeRepo siblings) and guarded to
// the legal transitions: proposed -> approved/dismissed and
// approved/dismissed -> proposed (the undo). The moment is addressed by
// (episode, rank) — its stable natural key. ok=false (nil error) when nothing
// was flipped: an unknown/foreign episode, an unknown rank, or an illegal
// transition (e.g. approved -> dismissed, or a same-status no-op) — the
// caller renders that as a refusal, never a 500. An unknown target status is
// a programming error and is rejected before touching the database.
func (s *Store) SetMomentStatus(ctx context.Context, orgPublicID, episodePublicID string, rank int, status string) (bool, error) {
	switch status {
	case MomentStatusProposed, MomentStatusApproved, MomentStatusDismissed:
	default:
		return false, fmt.Errorf("store: unknown moment status %q", status)
	}
	ep, ok, err := s.resolveEpisodeForReview(ctx, orgPublicID, episodePublicID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	_, err = s.TransitionMomentStatus(ctx, db.TransitionMomentStatusParams{
		Status:    status,
		EpisodeID: ep.ID,
		Rank:      int32(rank),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// No such rank, or an illegal transition: a clean refusal.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: set moment status: %w", err)
	}
	return true, nil
}

// TranscriptForCompose returns the episode's speaker-aware transcript
// (idx-ordered), its content language, and the INTERNAL org/episode ids the
// compose llm_calls audit is scoped by — the free-prompt compose read. It
// serves the api compose surface, so it resolves the episode with the REVIEW
// dialect (the session principal's canonical org UUID, like EpisodeMoments),
// not the pipeline's encoded ids. ok=false (nil error) for an unknown/foreign
// episode — the handler's indistinguishable 404. A found episode with no
// segments yet returns an empty Segments slice (the caller's "not transcribed
// yet" refusal).
func (s *Store) TranscriptForCompose(ctx context.Context, orgPublicID, episodePublicID string) (pipeline.MomentSegmentSet, string, bool, error) {
	ep, ok, err := s.resolveEpisodeForReview(ctx, orgPublicID, episodePublicID)
	if err != nil {
		return pipeline.MomentSegmentSet{}, "", false, err
	}
	if !ok {
		return pipeline.MomentSegmentSet{}, "", false, nil
	}
	rows, err := s.ListSegmentsByEpisode(ctx, ep.ID)
	if err != nil {
		return pipeline.MomentSegmentSet{}, "", false, fmt.Errorf("store: list segments for compose: %w", err)
	}
	segs := make([]pipeline.MomentSegment, 0, len(rows))
	for _, r := range rows {
		words, derr := decodeWords(r.Words)
		if derr != nil {
			return pipeline.MomentSegmentSet{}, "", false, derr
		}
		segs = append(segs, pipeline.MomentSegment{
			Segment: asr.Segment{
				Idx:     int(r.Idx),
				StartMs: int(r.StartMs),
				EndMs:   int(r.EndMs),
				Text:    r.Text,
				Words:   words,
			},
			SpeakerKey: r.SpeakerKey.String, // pgtype.Text zero value -> "" when NULL
		})
	}
	return pipeline.MomentSegmentSet{OrgID: ep.OrgID, EpisodeID: ep.ID, Segments: segs}, ep.Language, true, nil
}

// insertComposedAttempts bounds the keep insert's races: rank = max(rank)+1 is
// computed atomically per statement, but two concurrent keeps can still
// compute the same next rank; the loser's unique violation is retried afresh.
const insertComposedAttempts = 3

// InsertComposedMoment persists one KEPT user-composed moment (approve-to-keep)
// at the episode's next free rank with source='prompt' and status='approved'
// (keeping IS the approval; status_changed_at is stamped). Org-scoped with the
// review dialect like TranscriptForCompose; ok=false (nil error) for an
// unknown/foreign episode. The caller (the compose seam) has already
// re-validated the quote verbatim against the CURRENT transcript and re-derived
// the word-accurate times — this method only persists. A concurrent-keep rank
// race is retried a bounded number of times, never looped unboundedly.
func (s *Store) InsertComposedMoment(ctx context.Context, orgPublicID, episodePublicID string, row pipeline.MomentRow) (api.EpisodeMoment, bool, error) {
	ep, ok, err := s.resolveEpisodeForReview(ctx, orgPublicID, episodePublicID)
	if err != nil {
		return api.EpisodeMoment{}, false, err
	}
	if !ok {
		return api.EpisodeMoment{}, false, nil
	}
	var lastErr error
	for attempt := 0; attempt < insertComposedAttempts; attempt++ {
		r, err := s.InsertPromptMoment(ctx, db.InsertPromptMomentParams{
			EpisodeID:   ep.ID,
			StartIdx:    int32(row.StartIdx),
			EndIdx:      int32(row.EndIdx),
			StartMs:     int32(row.StartMs),
			EndMs:       int32(row.EndMs),
			RationaleEn: row.RationaleEn,
			QuoteFa:     row.QuoteFa,
		})
		if isUniqueViolation(err) {
			lastErr = err
			continue // a concurrent keep took the rank; recompute and retry
		}
		if err != nil {
			return api.EpisodeMoment{}, false, fmt.Errorf("store: insert composed moment: %w", err)
		}
		return api.EpisodeMoment{
			Rank:            int(r.Rank),
			StartIdx:        int(r.StartIdx),
			EndIdx:          int(r.EndIdx),
			StartMs:         int(r.StartMs),
			EndMs:           int(r.EndMs),
			RationaleEn:     r.RationaleEn,
			QuoteFa:         r.QuoteFa,
			Status:          r.Status,
			StatusChangedAt: r.StatusChangedAt.Time,
		}, true, nil
	}
	return api.EpisodeMoment{}, false, fmt.Errorf("store: insert composed moment: rank contention persisted: %w", lastErr)
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505) — the composed-rank race signal.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
