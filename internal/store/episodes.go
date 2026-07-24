package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"blueshift/internal/api"
	"blueshift/internal/ids"
	"blueshift/internal/store/db"
)

// Store implements the api episode persistence port.
var _ api.EpisodeRepo = (*Store)(nil)

// CreateEpisode inserts an episode for the principal's org (resolved from the
// org public id, never from client input), hanging it off the org's default
// show, status 'uploaded', with no master key yet. It records the declared
// master size so upload-complete can reject a short upload.
func (s *Store) CreateEpisode(ctx context.Context, orgPublicID string, in api.NewEpisode) (api.EpisodeRow, error) {
	org, err := s.resolveOrg(ctx, orgPublicID)
	if err != nil {
		return api.EpisodeRow{}, err
	}
	show, err := s.GetDefaultShowForOrg(ctx, org.ID)
	if err != nil {
		return api.EpisodeRow{}, fmt.Errorf("store: default show for org: %w", err)
	}
	ep, err := s.InsertEpisode(ctx, db.InsertEpisodeParams{
		OrgID:           org.ID,
		ShowID:          show.ID,
		Title:           in.Title,
		SourceFilename:  in.SourceFilename,
		Language:        in.Language,
		MasterObjectKey: pgtype.Text{}, // set at upload-complete
		MasterSizeBytes: pgtype.Int8{Int64: in.SizeBytes, Valid: in.SizeBytes > 0},
	})
	if err != nil {
		return api.EpisodeRow{}, fmt.Errorf("store: insert episode: %w", err)
	}
	return episodeRow(org.PublicID, ep), nil
}

// DeleteOrphanEpisode hard-deletes a just-created episode row when the create
// failed before an upload URL could be minted. It is org-scoped and gated on the
// fresh-orphan shape (status 'uploaded', no master key) in SQL, so it can only
// ever remove an unreachable half-created row. A malformed id or a row that no
// longer matches the gate is a silent no-op (nil error): there is nothing to
// roll back and nothing to report.
func (s *Store) DeleteOrphanEpisode(ctx context.Context, orgPublicID, episodePublicID string) error {
	org, err := s.resolveOrg(ctx, orgPublicID)
	if err != nil {
		return err
	}
	epUUID, err := ids.Decode(ids.Episode, episodePublicID)
	if err != nil {
		return nil
	}
	if _, err := s.Queries.DeleteOrphanEpisode(ctx, db.DeleteOrphanEpisodeParams{
		PublicID: pgUUID(epUUID),
		OrgID:    org.ID,
	}); err != nil {
		return fmt.Errorf("store: delete orphan episode: %w", err)
	}
	return nil
}

// DeleteEpisode soft-deletes an org-scoped episode by stamping deleted_at (the
// row is kept; every read/claim/finalize/sweep path filters deleted_at IS NULL,
// so the episode becomes invisible to the API and unclaimable/unbillable by the
// pipeline). found=false when the id is malformed or names no row in the org —
// the handler's 404. Idempotent: an already-deleted row still reports
// found=true (its original deleted_at is preserved), so a repeated DELETE stays
// a 204. Storage objects are NOT removed — soft delete is row-level only, and
// object GC for deleted episodes is a deliberate later concern.
func (s *Store) DeleteEpisode(ctx context.Context, orgPublicID, episodePublicID string) (bool, error) {
	org, err := s.resolveOrg(ctx, orgPublicID)
	if err != nil {
		return false, err
	}
	epUUID, err := ids.Decode(ids.Episode, episodePublicID)
	if err != nil {
		return false, nil
	}
	n, err := s.SoftDeleteEpisode(ctx, db.SoftDeleteEpisodeParams{
		PublicID: pgUUID(epUUID),
		OrgID:    org.ID,
	})
	if err != nil {
		return false, fmt.Errorf("store: soft delete episode: %w", err)
	}
	return n > 0, nil
}

// SweepAbandonedEpisodes hard-deletes abandoned uploads across ALL orgs: rows a
// create left at 'uploaded' with no master key whose client never completed the
// PUT, older than ttl. This is a system-level maintenance sweep (not a tenant
// action), so it is deliberately NOT org-scoped — the narrow orphan gate
// (status 'uploaded', no master key) plus the age floor, enforced in SQL, is
// what keeps it from ever removing a live or advanced episode. It returns the
// number of rows removed so the caller can log a non-zero sweep. This method
// shadows the promoted db.Queries.SweepAbandonedEpisodes, adapting the TTL from
// a Go duration to the interval the query expects.
func (s *Store) SweepAbandonedEpisodes(ctx context.Context, ttl time.Duration) (int64, error) {
	n, err := s.Queries.SweepAbandonedEpisodes(ctx, pgtype.Interval{
		Microseconds: ttl.Microseconds(),
		Valid:        true,
	})
	if err != nil {
		return 0, fmt.Errorf("store: sweep abandoned episodes: %w", err)
	}
	return n, nil
}

// SweepStuckProcessingEpisodes force-fails, across ALL orgs, episodes stuck in
// 'processing' whose claim is older than ttl (or whose claimed_at is NULL — a
// legacy claim, including the currently-stuck prod rows). It is the backstop for
// a worker that claimed an episode and then died (SIGKILL/OOM/crash) without
// finalizing it: Cloud Run reports that execution "succeeded", so nothing else
// ever moves the row off 'processing' and the retry API (which only accepts
// 'failed') cannot rescue it. Like SweepAbandonedEpisodes this is a system-level
// maintenance sweep, deliberately NOT org-scoped; its safety comes from the
// narrow gate ('processing' + the claim-age floor) enforced in SQL. Each swept
// row is stamped a neutral error_id (server-side correlation only, never a client
// surface). Returns the number of rows failed so the caller can WARN.
func (s *Store) SweepStuckProcessingEpisodes(ctx context.Context, ttl time.Duration) (int64, error) {
	n, err := s.Queries.SweepStuckProcessingEpisodes(ctx, db.SweepStuckProcessingEpisodesParams{
		ErrorID: pgtype.Text{String: neutralErrorID(), Valid: true},
		Ttl: pgtype.Interval{
			Microseconds: ttl.Microseconds(),
			Valid:        true,
		},
	})
	if err != nil {
		return 0, fmt.Errorf("store: sweep stuck processing episodes: %w", err)
	}
	return n, nil
}

// EpisodeStatus returns an episode's current status by public id, not org-scoped
// (the worker has no org before it claims, exactly like ClaimEpisodeForStage).
// It exists only to annotate the server-side WARN a worker logs when it cannot
// take a claim — the blocking status is *why* the claim was refused. A malformed
// id or a missing/soft-deleted row yields ("", nil): a clean "nothing to claim",
// not a fault. Server-log-only; the status string never reaches a client.
func (s *Store) EpisodeStatus(ctx context.Context, episodePublicID string) (string, error) {
	epUUID, err := ids.Decode(ids.Episode, episodePublicID)
	if err != nil {
		return "", nil
	}
	status, err := s.GetEpisodeStatusByPublicID(ctx, pgUUID(epUUID))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: episode status: %w", err)
	}
	return status, nil
}

// neutralErrorID returns a short random hex id that correlates a server-side
// failure with the log line that recorded it. It names nothing — no provider,
// no tool, no cause. It mirrors the pipeline's own error-id shape (16 hex chars).
func neutralErrorID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// GetEpisode fetches an org-scoped episode by its public id. A malformed id or a
// row belonging to another org both report found=false (a 404), so nothing about
// another org's data is observable.
func (s *Store) GetEpisode(ctx context.Context, orgPublicID, episodePublicID string) (api.EpisodeRow, bool, error) {
	org, err := s.resolveOrg(ctx, orgPublicID)
	if err != nil {
		return api.EpisodeRow{}, false, err
	}
	epUUID, err := ids.Decode(ids.Episode, episodePublicID)
	if err != nil {
		return api.EpisodeRow{}, false, nil
	}
	ep, err := s.GetEpisodeByPublicID(ctx, db.GetEpisodeByPublicIDParams{
		PublicID: pgUUID(epUUID),
		OrgID:    org.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return api.EpisodeRow{}, false, nil
	}
	if err != nil {
		return api.EpisodeRow{}, false, fmt.Errorf("store: get episode: %w", err)
	}
	return episodeRow(org.PublicID, ep), true, nil
}

// EpisodeTranscript returns an episode's transcript segments in idx order,
// projected to the neutral api.TranscriptSegment shape (verbatim text + word
// timings + the additive speaker_key, "" until diarized). It is org-scoped and
// resolves the org exactly like GetEpisode does — by the principal's org public
// id (a canonical UUID from the session), NOT the base32-encoded id the
// pipeline's segment methods use — so the id contract matches its EpisodeRepo
// siblings. An unknown/foreign org or an episode not visible to the org yields an
// empty slice (no error): the handler establishes existence (404 vs empty) via
// GetEpisode, so a lost race here degrades to an empty transcript, never a fault.
//
// Verbatim invariant (CLAUDE.md): text and word tuples are surfaced exactly as
// stored (decodeWords preserves U+200C ZWNJ byte-for-byte); this read never
// normalizes or rewrites the transcript.
func (s *Store) EpisodeTranscript(ctx context.Context, orgPublicID, episodePublicID string) ([]api.TranscriptSegment, error) {
	org, err := s.resolveOrg(ctx, orgPublicID)
	if err != nil {
		return nil, err
	}
	epUUID, err := ids.Decode(ids.Episode, episodePublicID)
	if err != nil {
		return nil, nil
	}
	ep, err := s.GetEpisodeByPublicID(ctx, db.GetEpisodeByPublicIDParams{
		PublicID: pgUUID(epUUID),
		OrgID:    org.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: resolve episode for transcript: %w", err)
	}
	rows, err := s.ListSegmentsByEpisode(ctx, ep.ID)
	if err != nil {
		return nil, fmt.Errorf("store: list transcript segments: %w", err)
	}
	out := make([]api.TranscriptSegment, 0, len(rows))
	for _, r := range rows {
		words, derr := decodeWords(r.Words)
		if derr != nil {
			return nil, derr
		}
		ws := make([]api.TranscriptWord, 0, len(words))
		for _, w := range words {
			ws = append(ws, api.TranscriptWord{Text: w.Text, StartMs: w.StartMs, EndMs: w.EndMs, Conf: w.Conf})
		}
		out = append(out, api.TranscriptSegment{
			Idx:        int(r.Idx),
			StartMs:    int(r.StartMs),
			EndMs:      int(r.EndMs),
			Text:       r.Text,
			SpeakerKey: r.SpeakerKey.String, // pgtype.Text zero value -> "" when NULL
			Words:      ws,
		})
	}
	return out, nil
}

// SetEpisodeMasterKey records the verified master object key on an org-scoped
// episode. found=false when the episode is not visible to the org.
func (s *Store) SetEpisodeMasterKey(ctx context.Context, orgPublicID, episodePublicID, key string) (api.EpisodeRow, bool, error) {
	org, err := s.resolveOrg(ctx, orgPublicID)
	if err != nil {
		return api.EpisodeRow{}, false, err
	}
	epUUID, err := ids.Decode(ids.Episode, episodePublicID)
	if err != nil {
		return api.EpisodeRow{}, false, nil
	}
	// Call the generated query explicitly: this method shadows the promoted
	// db.Queries.SetEpisodeMasterKey of the same name.
	ep, err := s.Queries.SetEpisodeMasterKey(ctx, db.SetEpisodeMasterKeyParams{
		PublicID:        pgUUID(epUUID),
		OrgID:           org.ID,
		MasterObjectKey: pgtype.Text{String: key, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return api.EpisodeRow{}, false, nil
	}
	if err != nil {
		return api.EpisodeRow{}, false, fmt.Errorf("store: set master key: %w", err)
	}
	return episodeRow(org.PublicID, ep), true, nil
}

// resolveOrg turns the principal's org public id (canonical UUID string) into
// the internal org row. A malformed id or a missing org is a server-side
// inconsistency for an already-authenticated caller, so it is an error (mapped
// to 503), never a 404.
func (s *Store) resolveOrg(ctx context.Context, orgPublicID string) (db.Org, error) {
	u, err := parseUUID(orgPublicID)
	if err != nil {
		return db.Org{}, fmt.Errorf("store: bad org public id: %w", err)
	}
	org, err := s.GetOrgByPublicID(ctx, pgUUID(u))
	if err != nil {
		return db.Org{}, fmt.Errorf("store: resolve org: %w", err)
	}
	return org, nil
}

// ListEpisodes returns the org's episodes newest-first (soft-deleted excluded),
// scoped by the resolved org id — never by client input, so a caller can only
// ever see their own org's rows.
func (s *Store) ListEpisodes(ctx context.Context, orgPublicID string) ([]api.EpisodeRow, error) {
	org, err := s.resolveOrg(ctx, orgPublicID)
	if err != nil {
		return nil, err
	}
	eps, err := s.ListEpisodesByOrg(ctx, org.ID)
	if err != nil {
		return nil, fmt.Errorf("store: list episodes: %w", err)
	}
	out := make([]api.EpisodeRow, 0, len(eps))
	for _, ep := range eps {
		out = append(out, episodeRow(org.PublicID, ep))
	}
	return out, nil
}

// RetryEpisode compare-and-sets a 'failed' episode back to 'uploaded'. It is
// org-scoped and gated on status = 'failed'; when no such row matches (wrong
// org, missing, or not failed) it reports retried=false so the handler returns
// a 409, never touching another org's data.
func (s *Store) RetryEpisode(ctx context.Context, orgPublicID, episodePublicID string) (api.EpisodeRow, bool, error) {
	org, err := s.resolveOrg(ctx, orgPublicID)
	if err != nil {
		return api.EpisodeRow{}, false, err
	}
	epUUID, err := ids.Decode(ids.Episode, episodePublicID)
	if err != nil {
		return api.EpisodeRow{}, false, nil
	}
	ep, err := s.RetryFailedEpisode(ctx, db.RetryFailedEpisodeParams{
		PublicID: pgUUID(epUUID),
		OrgID:    org.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return api.EpisodeRow{}, false, nil
	}
	if err != nil {
		return api.EpisodeRow{}, false, fmt.Errorf("store: retry episode: %w", err)
	}
	return episodeRow(org.PublicID, ep), true, nil
}

func episodeRow(orgPub pgtype.UUID, ep db.Episode) api.EpisodeRow {
	return api.EpisodeRow{
		OrgPublicID:    orgPub.Bytes,
		PublicID:       ep.PublicID.Bytes,
		Title:          ep.Title,
		SourceFilename: ep.SourceFilename,
		Language:       ep.Language,
		Status:         ep.Status,
		CurrentStage:   textOrEmpty(ep.CurrentStage),
		SizeBytes:      int64OrZero(ep.MasterSizeBytes),
		DurationMs:     int64OrZero(ep.DurationMs),
		MasterKey:      textOrEmpty(ep.MasterObjectKey),
		ProxyKey:       textOrEmpty(ep.ProxyObjectKey),
		CreatedAt:      ep.CreatedAt.Time,
	}
}

func pgUUID(b [16]byte) pgtype.UUID { return pgtype.UUID{Bytes: b, Valid: true} }

func int64OrZero(v pgtype.Int8) int64 {
	if !v.Valid {
		return 0
	}
	return v.Int64
}

func textOrEmpty(v pgtype.Text) string {
	if !v.Valid {
		return ""
	}
	return v.String
}

// parseUUID parses a canonical 8-4-4-4-12 hyphenated UUID string into its 16
// bytes. It is the inverse of uuidString.
func parseUUID(s string) ([16]byte, error) {
	var out [16]byte
	clean := strings.ReplaceAll(s, "-", "")
	if len(clean) != 32 {
		return out, fmt.Errorf("uuid: want 32 hex digits, got %d", len(clean))
	}
	if _, err := hex.Decode(out[:], []byte(clean)); err != nil {
		return out, fmt.Errorf("uuid: %w", err)
	}
	return out, nil
}
