package store

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

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

func episodeRow(orgPub pgtype.UUID, ep db.Episode) api.EpisodeRow {
	return api.EpisodeRow{
		OrgPublicID:    orgPub.Bytes,
		PublicID:       ep.PublicID.Bytes,
		Title:          ep.Title,
		SourceFilename: ep.SourceFilename,
		Language:       ep.Language,
		Status:         ep.Status,
		SizeBytes:      int64OrZero(ep.MasterSizeBytes),
		MasterKey:      textOrEmpty(ep.MasterObjectKey),
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
