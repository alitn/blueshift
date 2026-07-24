package store

// diarize.go is the segment store's diarization surface: reading the transcript
// with its audit scope for the diarize stage, and persisting the episode-local
// speaker_key the stage produces. It is additive over the transcribe-era segment
// store (segments.go): the read here surfaces the additive speaker_key column,
// and the write touches ONLY that column.
//
// Verbatim invariant (CLAUDE.md): SetSegmentSpeakers never writes text, words, or
// any *_ms timing — the diarize stage decides speaker grouping and nothing else.
// Both operations are org-scoped exactly like ReplaceSegments/EpisodeSegments, so
// a re-driven stage can never read or write across tenants.

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"blueshift/internal/asr"
	"blueshift/internal/pipeline"
	"blueshift/internal/store/db"
)

// The store is the production SpeakerStore for the diarize stage.
var _ pipeline.SpeakerStore = (*Store)(nil)

// SegmentWithSpeaker is a transcript segment plus its diarization speaker_key
// ("" when NULL — not yet diarized). It surfaces the additive speaker_key column
// alongside the verbatim transcript for diarization-aware readers, without
// putting diarization state on the ASR boundary type (asr.Segment stays the pure
// transcription shape).
type SegmentWithSpeaker struct {
	asr.Segment
	SpeakerKey string
}

// resolveEpisodeForSegments resolves (org public id, episode public id) to the
// internal org id and the episode row, org-scoped. ok=false (nil error) for an
// unknown/foreign org or an episode that resolves to no row for that org — the
// same lost-race/cross-tenant contract the other segment methods use.
func (s *Store) resolveEpisodeForSegments(ctx context.Context, orgID, episodePublicID string) (orgInternal int64, ep db.Episode, ok bool, err error) {
	orgInternal, epUUID, ok, err := s.resolveOrgAndEpisode(ctx, orgID, episodePublicID)
	if err != nil {
		return 0, db.Episode{}, false, err
	}
	if !ok {
		return 0, db.Episode{}, false, nil
	}
	ep, err = s.GetEpisodeByPublicID(ctx, db.GetEpisodeByPublicIDParams{
		PublicID: pgUUID(epUUID),
		OrgID:    orgInternal,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, db.Episode{}, false, nil
	}
	if err != nil {
		return 0, db.Episode{}, false, fmt.Errorf("store: resolve episode for segments: %w", err)
	}
	return orgInternal, ep, true, nil
}

// SegmentsForDiarize returns the episode's transcript (idx-ordered) together with
// the internal org/episode ids the diarize stage scopes its llm_calls audit by,
// all org-scoped. found=false (nil error) for an unknown/foreign episode — a
// clean skip, never an error. It implements pipeline.SpeakerStore's read.
func (s *Store) SegmentsForDiarize(ctx context.Context, orgID, episodePublicID string) (pipeline.SegmentSet, bool, error) {
	orgInternal, ep, ok, err := s.resolveEpisodeForSegments(ctx, orgID, episodePublicID)
	if err != nil {
		return pipeline.SegmentSet{}, false, err
	}
	if !ok {
		return pipeline.SegmentSet{}, false, nil
	}
	rows, err := s.ListSegmentsByEpisode(ctx, ep.ID)
	if err != nil {
		return pipeline.SegmentSet{}, false, fmt.Errorf("store: list segments: %w", err)
	}
	segs := make([]asr.Segment, 0, len(rows))
	for _, r := range rows {
		words, derr := decodeWords(r.Words)
		if derr != nil {
			return pipeline.SegmentSet{}, false, derr
		}
		segs = append(segs, asr.Segment{
			Idx:     int(r.Idx),
			StartMs: int(r.StartMs),
			EndMs:   int(r.EndMs),
			Text:    r.Text,
			Words:   words,
		})
	}
	return pipeline.SegmentSet{OrgID: orgInternal, EpisodeID: ep.ID, Segments: segs}, true, nil
}

// SpeakersAssigned reports whether the episode is already fully diarized — it has
// segments AND every one carries a speaker_key — org-scoped. It is the diarize
// stage's cost-safety idempotency probe (CLAUDE.md "Billable-service cost safety"):
// a true result means the speaker grouping already exists, so the stage SKIPS the
// billable LLM call entirely and never re-bills on a retry/re-drive. "Fully" (all
// segments, not just some) is deliberate: SetSegmentSpeakers writes every segment in
// one transaction, so a completed diarize leaves none NULL; a partial set (an
// interrupted prior run) is NOT treated as done, so the stage re-diarizes rather
// than leaving segments unattributed. An unknown/foreign org or missing episode
// yields false (no error), matching the other segment methods.
func (s *Store) SpeakersAssigned(ctx context.Context, orgID, episodePublicID string) (bool, error) {
	_, ep, ok, err := s.resolveEpisodeForSegments(ctx, orgID, episodePublicID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	c, err := s.CountEpisodeSegmentsAndSpeakers(ctx, ep.ID)
	if err != nil {
		return false, fmt.Errorf("store: count diarized segments: %w", err)
	}
	return c.Total > 0 && c.Diarized == c.Total, nil
}

// EpisodeSegmentsWithSpeakers returns an episode's transcript in idx order with
// each segment's diarization speaker_key ("" when not yet diarized), org-scoped.
// An unknown/foreign org or missing episode yields nil (no error). It is the
// speaker-aware read for diarization-aware consumers and tests.
func (s *Store) EpisodeSegmentsWithSpeakers(ctx context.Context, orgID, episodePublicID string) ([]SegmentWithSpeaker, error) {
	_, ep, ok, err := s.resolveEpisodeForSegments(ctx, orgID, episodePublicID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	rows, err := s.ListSegmentsByEpisode(ctx, ep.ID)
	if err != nil {
		return nil, fmt.Errorf("store: list segments: %w", err)
	}
	out := make([]SegmentWithSpeaker, 0, len(rows))
	for _, r := range rows {
		words, derr := decodeWords(r.Words)
		if derr != nil {
			return nil, derr
		}
		out = append(out, SegmentWithSpeaker{
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
	return out, nil
}

// SetSegmentSpeakers persists the diarize stage's idx -> speaker_key grouping for
// an episode idempotently. Within one transaction it stamps speaker_key on each
// named segment (by episode-internal id + idx), so a re-run overwrites the prior
// grouping wholesale rather than duplicating anything. It writes ONLY speaker_key
// — text, words, and timings are never touched (verbatim invariant).
//
// It is org-scoped: the episode is resolved by (org public id, episode public
// id), so a caller can only ever write speakers for its own tenant's episode. An
// unknown/foreign org, or an episode that resolves to no row for that org, is a
// clean no-op (nothing to write). Idxs are applied in sorted order so the update
// sequence is deterministic; a NULL/empty grouping is a no-op commit.
func (s *Store) SetSegmentSpeakers(ctx context.Context, orgID, episodePublicID string, byIdx map[int]string) error {
	_, ep, ok, err := s.resolveEpisodeForSegments(ctx, orgID, episodePublicID)
	if err != nil {
		return err
	}
	if !ok {
		// Unknown/foreign org: nothing to write. A clean no-op.
		return nil
	}

	idxs := make([]int, 0, len(byIdx))
	for idx := range byIdx {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin speakers tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.WithTx(tx)

	for _, idx := range idxs {
		if err := q.SetSegmentSpeaker(ctx, db.SetSegmentSpeakerParams{
			EpisodeID:  ep.ID,
			Idx:        int32(idx),
			SpeakerKey: pgtype.Text{String: byIdx[idx], Valid: true},
		}); err != nil {
			return fmt.Errorf("store: set speaker for segment %d: %w", idx, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit speakers tx: %w", err)
	}
	return nil
}
