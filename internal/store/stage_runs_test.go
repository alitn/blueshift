package store

// DB-backed tests for the stage-run provenance store: open-at-claim /
// close-at-finalize round trip, append-only history with latest-per-stage
// reads, the llm_calls cost linkage for LLM stages, org scoping, and the
// idempotent close. Skipped (like every DB-backed store test) when
// TEST_DATABASE_URL is unset.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"blueshift/internal/ids"
	"blueshift/internal/pipeline"
	"blueshift/internal/store/db"
)

// stageRunsFixture provisions the store, the pilot org, and one episode, and
// returns everything a stage-run test needs.
type stageRunsFixture struct {
	st         *Store
	orgID      int64
	orgEnc     string
	orgUUIDStr string
	ep         db.Episode
	epEnc      string
}

func newStageRunsFixture(t *testing.T, ctx context.Context) stageRunsFixture {
	t.Helper()
	st, err := Open(ctx, requireDB(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(st.Close)
	applyDevSeed(t, st, ctx)

	var orgID, showID int64
	var orgUUIDStr string
	if err := st.Pool().QueryRow(ctx, `SELECT id, public_id::text FROM orgs WHERE name = 'Blueshift Pilot'`).Scan(&orgID, &orgUUIDStr); err != nil {
		t.Fatalf("find org: %v", err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT id FROM shows WHERE org_id = $1 ORDER BY id LIMIT 1`, orgID).Scan(&showID); err != nil {
		t.Fatalf("find show: %v", err)
	}
	org, err := st.GetOrg(ctx, orgID)
	if err != nil {
		t.Fatalf("GetOrg: %v", err)
	}
	ep, err := st.InsertEpisode(ctx, db.InsertEpisodeParams{
		OrgID: orgID, ShowID: showID, Title: "Prov", SourceFilename: "m.mp4", Language: "fa",
		MasterObjectKey: pgtype.Text{String: "k/masters/m.mp4", Valid: true},
	})
	if err != nil {
		t.Fatalf("InsertEpisode: %v", err)
	}
	deleteEpisodeOnCleanup(t, st, ep.ID)
	return stageRunsFixture{
		st:         st,
		orgID:      orgID,
		orgEnc:     ids.Encode(ids.Org, org.PublicID.Bytes),
		orgUUIDStr: orgUUIDStr,
		ep:         ep,
		epEnc:      ids.Encode(ids.Episode, ep.PublicID.Bytes),
	}
}

// TestStageRunOpenCloseRoundTrip covers the worker's write path end to end:
// open at claim (timestamps only — duration is derived at read), close at
// finalize with facts, and the API-side read surfacing the public shape.
func TestStageRunOpenCloseRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newStageRunsFixture(t, ctx)

	runID, err := f.st.StartStageRun(ctx, f.orgEnc, f.epEnc, "ingest", "bs-media-1", "ffmpeg")
	if err != nil {
		t.Fatalf("StartStageRun: %v", err)
	}
	if runID == 0 {
		t.Fatal("StartStageRun returned runID 0 for a visible episode")
	}

	// The open row: started_at stamped, finished_at/outcome NULL (in flight).
	var started, finished pgtype.Timestamptz
	var outcome pgtype.Text
	if err := f.st.Pool().QueryRow(ctx,
		`SELECT started_at, finished_at, outcome FROM stage_runs WHERE id = $1`, runID,
	).Scan(&started, &finished, &outcome); err != nil {
		t.Fatalf("read open run: %v", err)
	}
	if !started.Valid || finished.Valid || outcome.Valid {
		t.Errorf("open run = started %v finished %v outcome %v; want started only", started.Valid, finished.Valid, outcome.Valid)
	}

	if err := f.st.FinishStageRun(ctx, runID, pipeline.StageRunFinish{
		Outcome: pipeline.RunOutcomeOK,
		Facts: pipeline.StageRunFacts{
			ItemsIn: 4, ItemsOut: 9, Attempt: 2, CostCents: 7,
			Params: []byte(`{"segment_gap_ms":700}`),
		},
	}); err != nil {
		t.Fatalf("FinishStageRun: %v", err)
	}

	runs, err := f.st.EpisodeStageRuns(ctx, f.orgUUIDStr, f.epEnc)
	if err != nil {
		t.Fatalf("EpisodeStageRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs len = %d, want 1", len(runs))
	}
	run := runs[0]
	if run.Stage != "ingest" || run.Outcome != "ok" || run.EngineLabel != "bs-media-1" {
		t.Errorf("run = %+v, want ingest/ok/bs-media-1", run)
	}
	if run.StartedAt.IsZero() || run.FinishedAt.IsZero() {
		t.Errorf("run timestamps = %v/%v, want both set", run.StartedAt, run.FinishedAt)
	}
	if d := run.FinishedAt.Sub(run.StartedAt); d < 0 {
		t.Errorf("derived duration = %v, want >= 0", d)
	}
	if run.CostCents == nil || *run.CostCents != 7 {
		t.Errorf("cost = %v, want 7 (explicit cost wins)", run.CostCents)
	}

	// A second close is an idempotent no-op: the outcome never flips.
	if err := f.st.FinishStageRun(ctx, runID, pipeline.StageRunFinish{Outcome: pipeline.RunOutcomeFailed}); err != nil {
		t.Fatalf("second FinishStageRun: %v", err)
	}
	runs, err = f.st.EpisodeStageRuns(ctx, f.orgUUIDStr, f.epEnc)
	if err != nil || len(runs) != 1 {
		t.Fatalf("re-read: %v (%d runs)", err, len(runs))
	}
	if runs[0].Outcome != "ok" {
		t.Errorf("outcome after double close = %q, want ok (closed runs are immutable)", runs[0].Outcome)
	}

	// The private engine detail is in the DB (server truth) but has no field on
	// the api port type — assert it landed where it belongs.
	var detail string
	if err := f.st.Pool().QueryRow(ctx, `SELECT engine_detail FROM stage_runs WHERE id = $1`, runID).Scan(&detail); err != nil {
		t.Fatalf("read engine_detail: %v", err)
	}
	if detail != "ffmpeg" {
		t.Errorf("engine_detail = %q, want ffmpeg", detail)
	}
}

// TestStageRunHistoryLatestWins asserts re-runs append history rows and the
// display read returns only the latest per stage — a failed first pass
// superseded by a green re-run reads green.
func TestStageRunHistoryLatestWins(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newStageRunsFixture(t, ctx)

	first, err := f.st.StartStageRun(ctx, f.orgEnc, f.epEnc, "transcribe", "bs-asr-2", "model@loc")
	if err != nil || first == 0 {
		t.Fatalf("first StartStageRun: id=%d err=%v", first, err)
	}
	if err := f.st.FinishStageRun(ctx, first, pipeline.StageRunFinish{Outcome: pipeline.RunOutcomeFailed}); err != nil {
		t.Fatalf("finish first: %v", err)
	}
	// Distinct started_at so DISTINCT ON ordering is deterministic even on a
	// coarse clock.
	if _, err := f.st.Pool().Exec(ctx, `UPDATE stage_runs SET started_at = started_at - interval '1 minute' WHERE id = $1`, first); err != nil {
		t.Fatalf("backdate first run: %v", err)
	}

	second, err := f.st.StartStageRun(ctx, f.orgEnc, f.epEnc, "transcribe", "bs-asr-2", "model@loc")
	if err != nil || second == 0 {
		t.Fatalf("second StartStageRun: id=%d err=%v", second, err)
	}
	if err := f.st.FinishStageRun(ctx, second, pipeline.StageRunFinish{Outcome: pipeline.RunOutcomeOK}); err != nil {
		t.Fatalf("finish second: %v", err)
	}

	var total int
	if err := f.st.Pool().QueryRow(ctx, `SELECT count(*) FROM stage_runs WHERE episode_id = $1`, f.ep.ID).Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 2 {
		t.Errorf("history rows = %d, want 2 (append-only, no rewrite)", total)
	}

	runs, err := f.st.EpisodeStageRuns(ctx, f.orgUUIDStr, f.epEnc)
	if err != nil {
		t.Fatalf("EpisodeStageRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Outcome != "ok" {
		t.Fatalf("latest per stage = %+v, want the single green re-run", runs)
	}
}

// TestStageRunLLMCostLinkage asserts an LLM stage's close links cost_cents from
// the llm_calls audit rows recorded during the run, while a non-LLM stage never
// does.
func TestStageRunLLMCostLinkage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newStageRunsFixture(t, ctx)

	insertCall := func(cost int32) {
		t.Helper()
		if _, err := f.st.InsertLlmCall(ctx, db.InsertLlmCallParams{
			OrgID:         f.orgID,
			EpisodeID:     pgtype.Int8{Int64: f.ep.ID, Valid: true},
			Model:         "bs-lm-1",
			PromptVersion: "v1",
			InputHash:     "h",
			CostCents:     pgtype.Int4{Int32: cost, Valid: true},
			Status:        pgtype.Text{String: "ok", Valid: true},
		}); err != nil {
			t.Fatalf("InsertLlmCall: %v", err)
		}
	}

	runID, err := f.st.StartStageRun(ctx, f.orgEnc, f.epEnc, "diarize", "bs-lm-1", "provider/model")
	if err != nil || runID == 0 {
		t.Fatalf("StartStageRun: id=%d err=%v", runID, err)
	}
	insertCall(3)
	insertCall(4)
	if err := f.st.FinishStageRun(ctx, runID, pipeline.StageRunFinish{Outcome: pipeline.RunOutcomeOK}); err != nil {
		t.Fatalf("FinishStageRun: %v", err)
	}
	runs, err := f.st.EpisodeStageRuns(ctx, f.orgUUIDStr, f.epEnc)
	if err != nil || len(runs) != 1 {
		t.Fatalf("EpisodeStageRuns: %v (%d)", err, len(runs))
	}
	if runs[0].CostCents == nil || *runs[0].CostCents != 7 {
		t.Errorf("diarize cost = %v, want 7 (linked from llm_calls)", runs[0].CostCents)
	}

	// A non-LLM stage never links audit costs: an ingest run closed with no
	// explicit cost stays honestly NULL even with llm_calls rows in its window.
	ingestID, err := f.st.StartStageRun(ctx, f.orgEnc, f.epEnc, "ingest", "bs-media-1", "ffmpeg")
	if err != nil || ingestID == 0 {
		t.Fatalf("StartStageRun ingest: id=%d err=%v", ingestID, err)
	}
	insertCall(5)
	if err := f.st.FinishStageRun(ctx, ingestID, pipeline.StageRunFinish{Outcome: pipeline.RunOutcomeOK}); err != nil {
		t.Fatalf("FinishStageRun ingest: %v", err)
	}
	var ingestCost pgtype.Int4
	if err := f.st.Pool().QueryRow(ctx, `SELECT cost_cents FROM stage_runs WHERE id = $1`, ingestID).Scan(&ingestCost); err != nil {
		t.Fatalf("read ingest cost: %v", err)
	}
	if ingestCost.Valid {
		t.Errorf("ingest cost = %d, want NULL (no llm linkage for non-LLM stages)", ingestCost.Int32)
	}
}

// TestStageRunOrgScoping asserts a foreign org can neither open provenance on
// another org's episode (runID 0, no error — the fail-safe no-op) nor read it.
func TestStageRunOrgScoping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newStageRunsFixture(t, ctx)

	// A second real tenant.
	var otherID int64
	var otherUUIDStr string
	if err := f.st.Pool().QueryRow(ctx,
		`INSERT INTO orgs (name) VALUES ('Blueshift Prov Two') RETURNING id, public_id::text`,
	).Scan(&otherID, &otherUUIDStr); err != nil {
		t.Fatalf("create second org: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = f.st.Pool().Exec(c, `DELETE FROM orgs WHERE id = $1`, otherID)
	})
	other, err := f.st.GetOrg(ctx, otherID)
	if err != nil {
		t.Fatalf("GetOrg other: %v", err)
	}
	otherEnc := ids.Encode(ids.Org, other.PublicID.Bytes)

	// Foreign open: clean no-op, nothing recorded.
	runID, err := f.st.StartStageRun(ctx, otherEnc, f.epEnc, "ingest", "bs-media-1", "ffmpeg")
	if err != nil {
		t.Fatalf("foreign StartStageRun errored: %v", err)
	}
	if runID != 0 {
		t.Errorf("foreign StartStageRun = %d, want 0 (no cross-tenant provenance)", runID)
	}

	// Owner records a run; the foreign org reads nothing.
	ownID, err := f.st.StartStageRun(ctx, f.orgEnc, f.epEnc, "ingest", "bs-media-1", "ffmpeg")
	if err != nil || ownID == 0 {
		t.Fatalf("owner StartStageRun: id=%d err=%v", ownID, err)
	}
	foreign, err := f.st.EpisodeStageRuns(ctx, otherUUIDStr, f.epEnc)
	if err != nil {
		t.Fatalf("foreign EpisodeStageRuns: %v", err)
	}
	if len(foreign) != 0 {
		t.Errorf("foreign read returned %d runs, want 0", len(foreign))
	}
}
