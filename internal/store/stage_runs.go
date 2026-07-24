package store

// stage_runs.go — stage-run provenance persistence: the worker's RunRecorder
// (open a history row at claim, close it at finalize) and the API's org-scoped
// latest-per-stage read. The record is timestamps + facts; duration is derived
// at read time by the API layer, never stored. engine_detail (the private
// provider truth) is written here and read back ONLY into the server-side
// generated row type — the api.StageRun port type has no field for it, so it
// structurally cannot reach a DTO.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"blueshift/internal/api"
	"blueshift/internal/ids"
	"blueshift/internal/pipeline"
	"blueshift/internal/store/db"
)

// The store is the production RunRecorder for the pipeline runner.
var _ pipeline.RunRecorder = (*Store)(nil)

// The store is the production StageRunReader for the API's pipeline endpoint.
var _ api.StageRunReader = (*Store)(nil)

// StartStageRun opens a stage-run provenance row for a just-claimed stage and
// returns its id. Append-only history: every claim inserts a NEW row (a re-run
// never rewrites an earlier one); display reads take the latest per stage.
// Org-scoped like every finalizer: an unknown/foreign org or an invisible
// episode returns runID 0 with no error — provenance is best-effort
// observability and must never fault a run. It runs AFTER the claim's
// compare-and-set, in its own statement, so claim atomicity is untouched.
func (s *Store) StartStageRun(ctx context.Context, orgID, episodePublicID, stage, engineLabel, engineDetail string) (int64, error) {
	_, ep, ok, err := s.resolveEpisodeForSegments(ctx, orgID, episodePublicID)
	if err != nil {
		return 0, err
	}
	if !ok {
		// Unknown/foreign org or invisible episode: nothing to record.
		return 0, nil
	}
	runID, err := s.InsertStageRun(ctx, db.InsertStageRunParams{
		EpisodeID:    ep.ID,
		Stage:        stage,
		EngineLabel:  pgtype.Text{String: engineLabel, Valid: engineLabel != ""},
		EngineDetail: pgtype.Text{String: engineDetail, Valid: engineDetail != ""},
	})
	if err != nil {
		return 0, fmt.Errorf("store: insert stage run: %w", err)
	}
	return runID, nil
}

// FinishStageRun closes a stage-run row: finished_at = now(), the outcome, and
// the run's facts (zero values persist as NULL — provenance never invents a
// number). runID 0 (no row was opened) is a clean no-op, as is a row already
// finished (the query is gated on finished_at IS NULL). For the LLM-backed
// stages the query links cost_cents from the llm_calls audit when no explicit
// cost is passed.
func (s *Store) FinishStageRun(ctx context.Context, runID int64, fin pipeline.StageRunFinish) error {
	if runID == 0 {
		return nil
	}
	_, err := s.Queries.FinishStageRun(ctx, db.FinishStageRunParams{
		ID:        runID,
		Outcome:   pgtype.Text{String: fin.Outcome, Valid: fin.Outcome != ""},
		CostCents: pgInt4IfPositive(fin.Facts.CostCents),
		ItemsIn:   pgInt4IfPositive(fin.Facts.ItemsIn),
		ItemsOut:  pgInt4IfPositive(fin.Facts.ItemsOut),
		Attempt:   pgInt4IfPositive(fin.Facts.Attempt),
		Params:    fin.Facts.Params,
	})
	if err != nil {
		return fmt.Errorf("store: finish stage run: %w", err)
	}
	return nil
}

// EpisodeStageRuns returns the episode's LATEST stage-run provenance row per
// stage, projected to the neutral api.StageRun port shape (no engine detail —
// the port type has no field for it). Org-scoped exactly like
// EpisodeTranscript: an unknown/foreign org or an invisible episode yields an
// empty slice (no error); the handler establishes existence (404 vs empty) via
// GetEpisode, so a legacy episode with no rows degrades to an empty view.
func (s *Store) EpisodeStageRuns(ctx context.Context, orgPublicID, episodePublicID string) ([]api.StageRun, error) {
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
		return nil, fmt.Errorf("store: resolve episode for stage runs: %w", err)
	}
	rows, err := s.LatestStageRuns(ctx, ep.ID)
	if err != nil {
		return nil, fmt.Errorf("store: list stage runs: %w", err)
	}
	out := make([]api.StageRun, 0, len(rows))
	for _, r := range rows {
		run := api.StageRun{
			Stage:       r.Stage,
			StartedAt:   timeOrZero(r.StartedAt),
			FinishedAt:  timeOrZero(r.FinishedAt),
			Outcome:     r.Outcome.String,
			EngineLabel: r.EngineLabel.String,
		}
		if r.CostCents.Valid {
			c := int(r.CostCents.Int32)
			run.CostCents = &c
		}
		out = append(out, run)
	}
	return out, nil
}

// pgInt4IfPositive maps the runner's "0 = unknown" int convention to a nullable
// int4: only a positive value is persisted, everything else records NULL.
func pgInt4IfPositive(v int) pgtype.Int4 {
	if v <= 0 {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(v), Valid: true} //nolint:gosec // counts/cents, far below int32 range
}

// timeOrZero unwraps a nullable timestamptz to time.Time (zero when NULL).
func timeOrZero(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}
