package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"blueshift/internal/ids"
	"blueshift/internal/pipeline"
	"blueshift/internal/store/db"
)

// Store implements the pipeline's episode persistence port.
var _ pipeline.Repo = (*Store)(nil)

// Claim is the stage-aware compare-and-set that takes an episode for stage and
// returns its identifiers. prevStage selects the shape: "" is an entry stage
// (ingest), advancing a single 'uploaded' episode to 'processing'; a non-empty
// prevStage is a continuation stage, claimable only from a 'processing' episode
// sitting at current_stage = prevStage (the prior stage's finalize left it
// there). Either way it stamps current_stage = stage and re-arms claimed_at.
// claimed=false (err=nil) when no row matches — already claimed, missing,
// terminal, or sitting at a different stage — so a losing/duplicate invocation
// cleanly no-ops. The org resolved here is the only org the finalizers below will
// accept, keeping every write in-tenant.
func (s *Store) Claim(ctx context.Context, episodePublicID, stage, prevStage string) (pipeline.Episode, bool, error) {
	epUUID, err := ids.Decode(ids.Episode, episodePublicID)
	if err != nil {
		// A malformed id names no episode: a clean no-op, not a fault.
		return pipeline.Episode{}, false, nil
	}
	row, err := s.ClaimEpisodeForStage(ctx, db.ClaimEpisodeForStageParams{
		PublicID:  pgUUID(epUUID),
		Stage:     pgtype.Text{String: stage, Valid: true},
		PrevStage: pgtype.Text{String: prevStage, Valid: prevStage != ""},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return pipeline.Episode{}, false, nil
	}
	if err != nil {
		return pipeline.Episode{}, false, fmt.Errorf("store: claim episode: %w", err)
	}
	org, err := s.GetOrg(ctx, row.OrgID)
	if err != nil {
		return pipeline.Episode{}, false, fmt.Errorf("store: resolve claimed org: %w", err)
	}
	return pipeline.Episode{
		OrgID:           ids.Encode(ids.Org, org.PublicID.Bytes),
		PublicID:        ids.Encode(ids.Episode, row.PublicID.Bytes),
		MasterObjectKey: textOrEmpty(row.MasterObjectKey),
		Language:        row.Language,
	}, true, nil
}

// AdvanceStage is the non-terminal stage finalize: it records the completing
// stage's outputs (proxy key + measured duration) and hands off to the next
// stage while the episode stays 'processing'. Org-scoped and gated on
// 'processing' + current_stage = completedStage, so a lost race, a foreign org,
// or a stage that no longer matches is an idempotent no-op — the same
// lost-race/cross-tenant contract as MarkReady/MarkFailed. current_stage is left
// at completedStage on purpose; the next stage's claim advances it (that
// transition is the continuation claim's compare-and-set). A zero durationMs or
// empty proxyKey leaves the existing column untouched (COALESCE).
func (s *Store) AdvanceStage(ctx context.Context, orgID, episodePublicID, completedStage, proxyObjectKey string, durationMs int64) error {
	orgInternal, epUUID, ok, err := s.resolveOrgAndEpisode(ctx, orgID, episodePublicID)
	if err != nil {
		return err
	}
	if !ok {
		// Unknown/foreign org: nothing to advance. A clean no-op.
		return nil
	}
	_, err = s.AdvanceEpisodeStage(ctx, db.AdvanceEpisodeStageParams{
		PublicID:       pgUUID(epUUID),
		OrgID:          orgInternal,
		CurrentStage:   pgtype.Text{String: completedStage, Valid: completedStage != ""},
		ProxyObjectKey: pgtype.Text{String: proxyObjectKey, Valid: proxyObjectKey != ""},
		DurationMs:     pgtype.Int8{Int64: durationMs, Valid: durationMs > 0},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: advance stage: %w", err)
	}
	return nil
}

// MarkReady finalizes a successful run: proxy key + measured duration, status
// 'ready'. Org-scoped and gated on 'processing'; a mismatch (wrong org, already
// finalized) is a no-op, so a lost race never corrupts another run or tenant.
func (s *Store) MarkReady(ctx context.Context, orgID, episodePublicID, proxyObjectKey string, durationMs int64) error {
	orgInternal, epUUID, ok, err := s.resolveOrgAndEpisode(ctx, orgID, episodePublicID)
	if err != nil {
		return err
	}
	if !ok {
		// Unknown/foreign org: it names no tenant we can finalize, so there is
		// nothing to write. A clean no-op, matching the lost-race contract.
		return nil
	}
	_, err = s.MarkEpisodeReady(ctx, db.MarkEpisodeReadyParams{
		PublicID:       pgUUID(epUUID),
		OrgID:          orgInternal,
		ProxyObjectKey: pgtype.Text{String: proxyObjectKey, Valid: proxyObjectKey != ""},
		DurationMs:     pgtype.Int8{Int64: durationMs, Valid: durationMs >= 0},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: mark ready: %w", err)
	}
	return nil
}

// MarkFailed finalizes an exhausted run: neutral error_id, status 'failed'.
// Same org-scoping and 'processing' gate as MarkReady.
func (s *Store) MarkFailed(ctx context.Context, orgID, episodePublicID, errorID string) error {
	orgInternal, epUUID, ok, err := s.resolveOrgAndEpisode(ctx, orgID, episodePublicID)
	if err != nil {
		return err
	}
	if !ok {
		// Unknown/foreign org: nothing to finalize. A clean no-op, matching the
		// lost-race contract.
		return nil
	}
	_, err = s.MarkEpisodeFailed(ctx, db.MarkEpisodeFailedParams{
		PublicID: pgUUID(epUUID),
		OrgID:    orgInternal,
		ErrorID:  pgtype.Text{String: errorID, Valid: errorID != ""},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: mark failed: %w", err)
	}
	return nil
}

// resolveOrgAndEpisode decodes the encoded public ids the pipeline carries back
// into the internal org id and episode uuid the finalizer queries need. ok is
// false (with a nil error) when the org public id, though well-formed, names no
// org: an unknown or foreign tenant is not a fault but a no-op, so a lost race
// or a cross-org id never errors the run — this is the same not-found semantic
// the rest of the package uses, and pgx.ErrNoRows must not leak past it. A
// malformed id remains a fault: the pipeline only ever round-trips ids it minted
// during Claim, so a bad one is a real bug, not untrusted input.
func (s *Store) resolveOrgAndEpisode(ctx context.Context, orgID, episodePublicID string) (int64, [16]byte, bool, error) {
	orgUUID, err := ids.Decode(ids.Org, orgID)
	if err != nil {
		return 0, [16]byte{}, false, fmt.Errorf("store: bad org id: %w", err)
	}
	org, err := s.GetOrgByPublicID(ctx, pgUUID(orgUUID))
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, [16]byte{}, false, nil
	}
	if err != nil {
		return 0, [16]byte{}, false, fmt.Errorf("store: resolve org: %w", err)
	}
	epUUID, err := ids.Decode(ids.Episode, episodePublicID)
	if err != nil {
		return 0, [16]byte{}, false, fmt.Errorf("store: bad episode id: %w", err)
	}
	return org.ID, epUUID, true, nil
}
