package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"blueshift/internal/asr"
	"blueshift/internal/store/db"
)

// ReplaceSegments persists an episode's transcript segments idempotently. Within
// one transaction it deletes the episode's existing segments and inserts the new
// set, so a re-run of the transcribe stage replaces the transcript wholesale
// rather than duplicating it (the UNIQUE(episode_id, idx) constraint would reject
// a naive re-insert anyway; delete-then-insert makes the re-run clean).
//
// It is org-scoped: the episode is resolved by (org public id, episode public
// id), so a caller can only ever write segments for its own tenant's episode. An
// unknown or foreign org, or an episode that resolves to no row for that org, is
// a clean no-op (nothing to write) — the same lost-race/cross-tenant contract the
// pipeline finalizers use.
//
// Verbatim invariant (CLAUDE.md): word text and timings are stored EXACTLY as the
// ASR engine returned them — no normalization at rest. Words are encoded as the
// positional jsonb array the schema documents: [text, start_ms, end_ms, conf].
func (s *Store) ReplaceSegments(ctx context.Context, orgID, episodePublicID string, segs []asr.Segment) error {
	orgInternal, epUUID, ok, err := s.resolveOrgAndEpisode(ctx, orgID, episodePublicID)
	if err != nil {
		return err
	}
	if !ok {
		// Unknown/foreign org: nothing to write. A clean no-op.
		return nil
	}
	ep, err := s.GetEpisodeByPublicID(ctx, db.GetEpisodeByPublicIDParams{
		PublicID: pgUUID(epUUID),
		OrgID:    orgInternal,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The org is known but names no episode we own: no-op, never an error.
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: resolve episode for segments: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin segments tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.WithTx(tx)

	if err := q.DeleteEpisodeSegments(ctx, ep.ID); err != nil {
		return fmt.Errorf("store: delete segments: %w", err)
	}
	for _, seg := range segs {
		words, err := encodeWords(seg.Words)
		if err != nil {
			return err
		}
		if err := q.InsertSegment(ctx, db.InsertSegmentParams{
			EpisodeID: ep.ID,
			Idx:       int32(seg.Idx),
			StartMs:   int32(seg.StartMs),
			EndMs:     int32(seg.EndMs),
			Text:      seg.Text,
			Words:     words,
		}); err != nil {
			return fmt.Errorf("store: insert segment %d: %w", seg.Idx, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit segments tx: %w", err)
	}
	return nil
}

// HasSegments reports whether the episode already has persisted transcript
// segments, org-scoped. It is the transcribe stage's cost-safety idempotency probe
// (CLAUDE.md "Billable-service cost safety"): a true result means the transcript
// already exists, so the stage SKIPS the billable ASR call entirely and never
// re-bills on a retry/re-drive. An unknown/foreign org or missing episode yields
// false (no error), matching ReplaceSegments' lost-race/cross-tenant contract — a
// re-drive that cannot resolve the episode simply is not "already transcribed".
func (s *Store) HasSegments(ctx context.Context, orgID, episodePublicID string) (bool, error) {
	orgInternal, epUUID, ok, err := s.resolveOrgAndEpisode(ctx, orgID, episodePublicID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	ep, err := s.GetEpisodeByPublicID(ctx, db.GetEpisodeByPublicIDParams{
		PublicID: pgUUID(epUUID),
		OrgID:    orgInternal,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: resolve episode for segment count: %w", err)
	}
	n, err := s.CountEpisodeSegments(ctx, ep.ID)
	if err != nil {
		return false, fmt.Errorf("store: count segments: %w", err)
	}
	return n > 0, nil
}

// EpisodeSegments returns an episode's transcript in idx order, org-scoped. It
// decodes the positional words jsonb back into asr.Word. An unknown/foreign org
// or missing episode yields nil (no error), matching ReplaceSegments.
func (s *Store) EpisodeSegments(ctx context.Context, orgID, episodePublicID string) ([]asr.Segment, error) {
	orgInternal, epUUID, ok, err := s.resolveOrgAndEpisode(ctx, orgID, episodePublicID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	ep, err := s.GetEpisodeByPublicID(ctx, db.GetEpisodeByPublicIDParams{
		PublicID: pgUUID(epUUID),
		OrgID:    orgInternal,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: resolve episode for segments: %w", err)
	}
	rows, err := s.ListSegmentsByEpisode(ctx, ep.ID)
	if err != nil {
		return nil, fmt.Errorf("store: list segments: %w", err)
	}
	out := make([]asr.Segment, 0, len(rows))
	for _, r := range rows {
		words, err := decodeWords(r.Words)
		if err != nil {
			return nil, err
		}
		out = append(out, asr.Segment{
			Idx:     int(r.Idx),
			StartMs: int(r.StartMs),
			EndMs:   int(r.EndMs),
			Text:    r.Text,
			Words:   words,
		})
	}
	return out, nil
}

// encodeWords serialises words to the positional jsonb array the segments.words
// column stores: an array of [text, start_ms, end_ms, conf] tuples, in word
// order. nil/empty words encode as an empty JSON array so the column is never
// NULL. The text is passed through verbatim (json.Marshal preserves the exact
// code points, including U+200C ZWNJ, as UTF-8).
func encodeWords(words []asr.Word) ([]byte, error) {
	arr := make([][]any, 0, len(words))
	for _, w := range words {
		arr = append(arr, []any{w.Text, w.StartMs, w.EndMs, w.Conf})
	}
	b, err := json.Marshal(arr)
	if err != nil {
		return nil, fmt.Errorf("store: encode words: %w", err)
	}
	return b, nil
}

// decodeWords is the inverse of encodeWords: it parses the positional jsonb array
// back into asr.Word values. It tolerates an empty/absent array (no words).
func decodeWords(raw []byte) ([]asr.Word, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var arr [][]json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("store: decode words: %w", err)
	}
	if len(arr) == 0 {
		return nil, nil
	}
	out := make([]asr.Word, 0, len(arr))
	for i, t := range arr {
		if len(t) != 4 {
			return nil, fmt.Errorf("store: decode words: tuple %d has %d fields, want 4", i, len(t))
		}
		var w asr.Word
		if err := json.Unmarshal(t[0], &w.Text); err != nil {
			return nil, fmt.Errorf("store: decode word %d text: %w", i, err)
		}
		if err := json.Unmarshal(t[1], &w.StartMs); err != nil {
			return nil, fmt.Errorf("store: decode word %d start: %w", i, err)
		}
		if err := json.Unmarshal(t[2], &w.EndMs); err != nil {
			return nil, fmt.Errorf("store: decode word %d end: %w", i, err)
		}
		if err := json.Unmarshal(t[3], &w.Conf); err != nil {
			return nil, fmt.Errorf("store: decode word %d conf: %w", i, err)
		}
		out = append(out, w)
	}
	return out, nil
}
