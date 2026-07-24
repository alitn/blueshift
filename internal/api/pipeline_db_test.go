package api_test

// DB-backed integration test for GET /api/episodes/{id}/pipeline. It wires the
// real router to a real *store.Store on the per-run scratch Postgres, records
// stage-run provenance through the PRODUCTION write path (store.StartStageRun /
// FinishStageRun — the same calls the worker's runner makes), and asserts the
// endpoint end to end: derived durations from real timestamps, the public
// engine labels, org scoping, legacy degradation, and — the vendor-neutrality
// core of this task — that the PRIVATE engine detail stored in the row never
// appears anywhere in the payload. Lives in package api_test (external) so it
// may import internal/store, like transcript_db_test.go.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blueshift/internal/api"
	"blueshift/internal/auth"
	"blueshift/internal/blob"
	"blueshift/internal/pipeline"
	"blueshift/internal/store"
)

// newPipelineRouter wires the real store as BOTH the episode repo and the
// stage-run reader, with the four-stage active chain the demo/prod deploys run.
func newPipelineRouter(t *testing.T, st *store.Store) http.Handler {
	t.Helper()
	local, err := blob.NewLocal(t.TempDir(), []byte("test-secret"), func() time.Time { return time.Unix(1_700_000_000, 0) })
	if err != nil {
		t.Fatalf("blob.NewLocal: %v", err)
	}
	return api.NewRouter(api.Deps{
		Episodes:       st,
		StageRuns:      st,
		PipelineStages: []string{"ingest", "transcribe", "diarize", "moments"},
		Blob:           local,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:            func() time.Time { return time.Unix(1_700_000_000, 0) },
	})
}

func getPipelineAs(router http.Handler, orgUUID, epID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/episodes/"+epID+"/pipeline", nil)
	req = req.WithContext(auth.NewContext(req.Context(), auth.Principal{Email: "e@x", OrgPublicID: orgUUID, Role: "editor"}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

type pipelineWire struct {
	Stages []struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		DurationMs *int64 `json:"duration_ms"`
		Engine     string `json:"engine"`
		CostCents  *int   `json:"cost_cents"`
	} `json:"stages"`
	QueuedMs *int64 `json:"queued_ms"`
	TotalMs  *int64 `json:"total_ms"`
}

// TestPipelineDBProvenanceReadThrough seeds runs through the production write
// path and asserts the neutral read-through, including the engine-detail
// negative: the provider truth stored in the row must never reach the wire.
func TestPipelineDBProvenanceReadThrough(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := openStore(t, ctx)
	orgUUID := pilotOrgUUID(t, ctx, st)
	orgEnc, epEnc, epUUID := seedEpisode(t, ctx, st, orgUUID)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = st.Pool().Exec(c, `DELETE FROM stage_runs WHERE episode_id = (SELECT id FROM episodes WHERE public_id = $1::uuid)`, epUUID)
	})

	// The exact worker choreography for two finished stages, with the PRIVATE
	// provider-shaped detail that must stay server-side. "acme-speech" is a
	// stand-in provider string; the negative below asserts it never surfaces.
	const privateDetail = "acme-speech-model-9@moon-east1"
	ingestID, err := st.StartStageRun(ctx, orgEnc, epEnc, "ingest", "bs-media-1", "ffmpeg")
	if err != nil || ingestID == 0 {
		t.Fatalf("StartStageRun ingest: id=%d err=%v", ingestID, err)
	}
	if err := st.FinishStageRun(ctx, ingestID, pipeline.StageRunFinish{Outcome: pipeline.RunOutcomeOK}); err != nil {
		t.Fatalf("FinishStageRun ingest: %v", err)
	}
	trID, err := st.StartStageRun(ctx, orgEnc, epEnc, "transcribe", "bs-asr-2", privateDetail)
	if err != nil || trID == 0 {
		t.Fatalf("StartStageRun transcribe: id=%d err=%v", trID, err)
	}
	if err := st.FinishStageRun(ctx, trID, pipeline.StageRunFinish{
		Outcome: pipeline.RunOutcomeOK,
		Facts:   pipeline.StageRunFacts{ItemsIn: 3, ItemsOut: 9, Attempt: 1, CostCents: 6},
	}); err != nil {
		t.Fatalf("FinishStageRun transcribe: %v", err)
	}
	// Widen the transcribe run so its derived duration is deterministic (>0).
	if _, err := st.Pool().Exec(ctx,
		`UPDATE stage_runs SET started_at = finished_at - interval '42 seconds' WHERE id = $1`, trID); err != nil {
		t.Fatalf("widen run: %v", err)
	}
	// The episode is mid-pipeline: processing at diarize.
	if _, err := st.Pool().Exec(ctx,
		`UPDATE episodes SET status = 'processing', current_stage = 'diarize' WHERE public_id = $1::uuid`, epUUID); err != nil {
		t.Fatalf("advance episode: %v", err)
	}

	router := newPipelineRouter(t, st)
	rec := getPipelineAs(router, orgUUID, epEnc)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var wire pipelineWire
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(wire.Stages) != 4 {
		t.Fatalf("stages = %d, want the 4 active-chain stages", len(wire.Stages))
	}
	byName := map[string]int{}
	for i, s := range wire.Stages {
		byName[s.Name] = i
	}
	tr := wire.Stages[byName["transcribe"]]
	if tr.Status != "done" || tr.Engine != "bs-asr-2" {
		t.Errorf("transcribe = %+v, want done under the public bs-asr-2 label", tr)
	}
	if tr.DurationMs == nil || *tr.DurationMs != 42_000 {
		t.Errorf("transcribe duration = %v, want 42000 (derived from the stored timestamps)", tr.DurationMs)
	}
	if tr.CostCents == nil || *tr.CostCents != 6 {
		t.Errorf("transcribe cost = %v, want 6", tr.CostCents)
	}
	if di := wire.Stages[byName["diarize"]]; di.Status != "active" {
		t.Errorf("diarize = %+v, want active", di)
	}
	if mo := wire.Stages[byName["moments"]]; mo.Status != "unreached" {
		t.Errorf("moments = %+v, want unreached", mo)
	}
	if wire.QueuedMs == nil || *wire.QueuedMs < 0 {
		t.Errorf("queued_ms = %v, want a non-negative value", wire.QueuedMs)
	}
	if wire.TotalMs == nil || *wire.TotalMs < 42_000 {
		t.Errorf("total_ms = %v, want >= 42000", wire.TotalMs)
	}

	// Neutrality negatives: the private detail, its fragments, and raw internal
	// shapes must never appear on the wire.
	lower := strings.ToLower(rec.Body.String())
	for _, bad := range []string{"engine_detail", "acme", "moon-east1", strings.ToLower(epUUID), "ffmpeg"} {
		if strings.Contains(lower, bad) {
			t.Errorf("response leaks %q: %s", bad, rec.Body.String())
		}
	}
}

// TestPipelineDBLegacyAndScoping covers the two remaining DB behaviours: a
// legacy episode (no stage_runs rows) degrades to the status-derived view with
// no timings, and a foreign org gets an indistinguishable 404.
func TestPipelineDBLegacyAndScoping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := openStore(t, ctx)
	orgUUID := pilotOrgUUID(t, ctx, st)
	_, epEnc, epUUID := seedEpisode(t, ctx, st, orgUUID)

	// Legacy shape: ready at moments with NO runs (processed before 0012).
	if _, err := st.Pool().Exec(ctx,
		`UPDATE episodes SET status = 'ready', current_stage = 'moments' WHERE public_id = $1::uuid`, epUUID); err != nil {
		t.Fatalf("mark legacy ready: %v", err)
	}

	router := newPipelineRouter(t, st)
	rec := getPipelineAs(router, orgUUID, epEnc)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy status = %d, want 200 (graceful degradation)", rec.Code)
	}
	var wire pipelineWire
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(wire.Stages) != 4 {
		t.Fatalf("legacy stages = %d, want 4", len(wire.Stages))
	}
	for _, s := range wire.Stages {
		if s.Status != "done" {
			t.Errorf("legacy stage %s = %q, want done (ready at the terminal stage)", s.Name, s.Status)
		}
		if s.DurationMs != nil || s.Engine != "" || s.CostCents != nil {
			t.Errorf("legacy stage %s carries run detail %+v, want none", s.Name, s)
		}
	}
	if wire.QueuedMs != nil || wire.TotalMs != nil {
		t.Errorf("legacy queued/total = %v/%v, want absent", wire.QueuedMs, wire.TotalMs)
	}

	// Cross-org: a second real tenant reads a 404, never the pilot's data.
	var otherUUID string
	if err := st.Pool().QueryRow(ctx, `INSERT INTO orgs (name) VALUES ('Blueshift Pipeline Two') RETURNING public_id::text`).Scan(&otherUUID); err != nil {
		t.Fatalf("create second org: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = st.Pool().Exec(c, `DELETE FROM orgs WHERE public_id = $1::uuid`, otherUUID)
	})
	if rec := getPipelineAs(router, otherUUID, epEnc); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-org status = %d, want 404", rec.Code)
	}
}
