package api

// Unit tests for GET /api/episodes/{id}/pipeline: the pure derivation
// (pipelineDTOFrom — legacy episodes, active/failed episodes, run enrichment,
// chain union, queued/total) and the handler contract (org-scoped 404, wire
// shape, graceful degradation without a provenance reader).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"blueshift/internal/auth"
	"blueshift/internal/blob"
	"blueshift/internal/ids"
)

// fakeStageRuns is a canned StageRunReader.
type fakeStageRuns struct {
	runs map[string][]StageRun // key: ep_ encoded public id
}

func (f *fakeStageRuns) EpisodeStageRuns(_ context.Context, _, epID string) ([]StageRun, error) {
	return f.runs[epID], nil
}

func intPtr(v int) *int { return &v }

var t0 = time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)

func okRun(stage string, startOffset, durMs int64, engine string, cost *int) StageRun {
	start := t0.Add(time.Duration(startOffset) * time.Millisecond)
	return StageRun{
		Stage: stage, StartedAt: start, FinishedAt: start.Add(time.Duration(durMs) * time.Millisecond),
		Outcome: "ok", EngineLabel: engine, CostCents: cost,
	}
}

func stageByName(t *testing.T, dto pipelineDTO, name string) pipelineStageDTO {
	t.Helper()
	for _, s := range dto.Stages {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("stage %q not in %+v", name, dto.Stages)
	return pipelineStageDTO{}
}

var fourStageChain = []string{"ingest", "transcribe", "diarize", "moments"}

// TestPipelineDTOLegacyNoRuns pins the graceful degradation: an episode with no
// stage_runs rows (processed before provenance landed) derives every status
// from status/current_stage — exactly the Library-bar ruling — with no
// durations, no engines, no queued/total.
func TestPipelineDTOLegacyNoRuns(t *testing.T) {
	cases := []struct {
		name, status, stage string
		want                map[string]string
	}{
		{"ready at moments", "ready", "moments", map[string]string{
			"ingest": "done", "transcribe": "done", "diarize": "done", "moments": "done"}},
		{"processing at transcribe", "processing", "transcribe", map[string]string{
			"ingest": "done", "transcribe": "active", "diarize": "unreached", "moments": "unreached"}},
		{"failed at diarize", "failed", "diarize", map[string]string{
			"ingest": "done", "transcribe": "done", "diarize": "failed", "moments": "unreached"}},
		{"queued", "uploaded", "", map[string]string{
			"ingest": "pending", "transcribe": "unreached", "diarize": "unreached", "moments": "unreached"}},
		{"legacy ready without stage", "ready", "", map[string]string{
			"ingest": "done", "transcribe": "unreached", "diarize": "unreached", "moments": "unreached"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := EpisodeRow{Status: c.status, CurrentStage: c.stage, CreatedAt: t0}
			dto := pipelineDTOFrom(row, nil, fourStageChain)
			if len(dto.Stages) != 4 {
				t.Fatalf("stages = %d, want the 4 active-chain stages", len(dto.Stages))
			}
			for name, want := range c.want {
				s := stageByName(t, dto, name)
				if s.Status != want {
					t.Errorf("%s status = %q, want %q", name, s.Status, want)
				}
				if s.DurationMs != nil || s.Engine != "" || s.CostCents != nil {
					t.Errorf("%s carries run detail %+v, want none (no runs)", name, s)
				}
			}
			if dto.QueuedMs != nil || dto.TotalMs != nil {
				t.Errorf("queued/total = %v/%v, want absent (no runs)", dto.QueuedMs, dto.TotalMs)
			}
		})
	}
}

// TestPipelineDTORunEnrichment pins the run-informed view: finished stages
// carry derived durations (timestamps only — duration computed here), the
// public engine label, and cost; queued_ms is upload -> first start; total_ms
// sums the finished durations.
func TestPipelineDTORunEnrichment(t *testing.T) {
	row := EpisodeRow{Status: "processing", CurrentStage: "diarize", CreatedAt: t0.Add(-2 * time.Second)}
	runs := []StageRun{
		okRun("ingest", 0, 1500, "bs-media-1", nil),
		okRun("transcribe", 2000, 40_000, "bs-asr-2", intPtr(12)),
		// Diarize is mid-flight: started, unfinished, no outcome.
		{Stage: "diarize", StartedAt: t0.Add(43 * time.Second), Outcome: "", EngineLabel: "bs-lm-1"},
	}
	dto := pipelineDTOFrom(row, runs, fourStageChain)

	ing := stageByName(t, dto, "ingest")
	if ing.Status != "done" || ing.Engine != "bs-media-1" || ing.DurationMs == nil || *ing.DurationMs != 1500 {
		t.Errorf("ingest = %+v, want done/bs-media-1/1500ms", ing)
	}
	tr := stageByName(t, dto, "transcribe")
	if tr.Status != "done" || tr.DurationMs == nil || *tr.DurationMs != 40_000 || tr.CostCents == nil || *tr.CostCents != 12 {
		t.Errorf("transcribe = %+v, want done/40000ms/12c", tr)
	}
	di := stageByName(t, dto, "diarize")
	if di.Status != "active" || di.Engine != "bs-lm-1" || di.DurationMs != nil {
		t.Errorf("diarize = %+v, want active/bs-lm-1/no duration (in flight)", di)
	}
	if mo := stageByName(t, dto, "moments"); mo.Status != "unreached" {
		t.Errorf("moments = %+v, want unreached", mo)
	}
	if dto.QueuedMs == nil || *dto.QueuedMs != 2000 {
		t.Errorf("queued = %v, want 2000 (upload -> first ingest start)", dto.QueuedMs)
	}
	if dto.TotalMs == nil || *dto.TotalMs != 41_500 {
		t.Errorf("total = %v, want 41500 (sum of finished durations)", dto.TotalMs)
	}
}

// TestPipelineDTOFailedAndSupersededRuns pins the failure rules: a failed run
// decorates the stage the episode actually failed at, while a stale failed run
// from an earlier pass never repaints a retried pipeline; an ok run marks its
// stage done even when current_stage sits earlier (idempotent outputs survive).
func TestPipelineDTOFailedAndSupersededRuns(t *testing.T) {
	// Episode failed at transcribe; its failed run carries duration + engine.
	failedRun := okRun("transcribe", 1000, 3000, "bs-asr-2", nil)
	failedRun.Outcome = "failed"
	row := EpisodeRow{Status: "failed", CurrentStage: "transcribe", CreatedAt: t0}
	dto := pipelineDTOFrom(row, []StageRun{okRun("ingest", 0, 500, "bs-media-1", nil), failedRun}, fourStageChain)
	tr := stageByName(t, dto, "transcribe")
	if tr.Status != "failed" || tr.Engine != "bs-asr-2" || tr.DurationMs == nil || *tr.DurationMs != 3000 {
		t.Errorf("failed transcribe = %+v, want failed/bs-asr-2/3000ms", tr)
	}

	// Retried pipeline back at ingest: the stale failed transcribe run must NOT
	// mark transcribe failed — the base derivation (unreached) wins.
	row2 := EpisodeRow{Status: "processing", CurrentStage: "ingest", CreatedAt: t0}
	dto2 := pipelineDTOFrom(row2, []StageRun{failedRun}, fourStageChain)
	if tr2 := stageByName(t, dto2, "transcribe"); tr2.Status != "unreached" || tr2.DurationMs != nil {
		t.Errorf("superseded failed run = %+v, want plain unreached", tr2)
	}

	// An ok run is authoritative even when the base says unreached (a re-driven
	// earlier stage): the produced output still exists.
	dto3 := pipelineDTOFrom(row2, []StageRun{okRun("transcribe", 0, 2000, "bs-asr-2", nil)}, fourStageChain)
	if tr3 := stageByName(t, dto3, "transcribe"); tr3.Status != "done" {
		t.Errorf("ok run under an earlier current_stage = %+v, want done", tr3)
	}
}

// TestPipelineDTOChainUnion pins the stage-list rule: the configured active
// chain is displayed even without runs, a stage that RAN is displayed even if
// the chain no longer names it, and an empty chain falls back to ingest-only.
func TestPipelineDTOChainUnion(t *testing.T) {
	row := EpisodeRow{Status: "ready", CurrentStage: "moments", CreatedAt: t0}
	// Chain shrunk to ingest-only (kill switch), but the episode ran all four.
	runs := []StageRun{
		okRun("ingest", 0, 500, "bs-media-1", nil),
		okRun("transcribe", 500, 1000, "bs-asr-2", nil),
		okRun("diarize", 1500, 1000, "bs-lm-1", nil),
		okRun("moments", 2500, 1000, "bs-lm-1", nil),
	}
	dto := pipelineDTOFrom(row, runs, []string{"ingest"})
	if len(dto.Stages) != 4 {
		t.Fatalf("stages = %d, want 4 (provenance beats configuration drift)", len(dto.Stages))
	}

	// Empty chain, no runs: the default ingest-only view.
	dto2 := pipelineDTOFrom(EpisodeRow{Status: "uploaded"}, nil, nil)
	if len(dto2.Stages) != 1 || dto2.Stages[0].Name != "ingest" || dto2.Stages[0].Status != "pending" {
		t.Errorf("default chain = %+v, want a single pending ingest", dto2.Stages)
	}

	// Canonical order is preserved regardless of run order.
	if dto.Stages[0].Name != "ingest" || dto.Stages[1].Name != "transcribe" || dto.Stages[2].Name != "diarize" || dto.Stages[3].Name != "moments" {
		t.Errorf("stage order = %+v, want canonical", dto.Stages)
	}
}

// seedEpisodeAs inserts an episode for org through the fake repo (no upload
// flow needed) and returns its encoded ep_ id.
func seedEpisodeAs(t *testing.T, repo *fakeRepo, org string) string {
	t.Helper()
	row, err := repo.CreateEpisode(context.Background(), org, NewEpisode{
		Title: "Ep", SourceFilename: "m.mp4", Language: "fa", SizeBytes: 10,
	})
	if err != nil {
		t.Fatalf("CreateEpisode: %v", err)
	}
	return ids.Encode(ids.Episode, row.PublicID)
}

// setStage stamps the fake row's current_stage (the fake repo predates the
// pipeline endpoint and has no setter for it).
func setStage(f *fakeRepo, epID, stage string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.eps[epID]
	s.row.CurrentStage = stage
	f.eps[epID] = s
}

// TestPipelineEndpointHandler drives the route end to end on the fakes: the
// org-scoped 404, the wire shape (exact keys; absent optionals stay absent),
// and graceful degradation when no provenance reader is wired.
func TestPipelineEndpointHandler(t *testing.T) {
	repo := newFakeRepo()
	router, _, blobSrv := newEpisodeRouter(t, repo)
	defer blobSrv.Close()

	epID := seedEpisodeAs(t, repo, orgA)
	repo.setStatus(epID, "ready", "k/proxies/p.mp4")

	// Foreign org: an indistinguishable 404.
	if rec := doAs(router, orgB, httptest.NewRequest(http.MethodGet, "/api/episodes/"+epID+"/pipeline", nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-org status = %d, want 404", rec.Code)
	}
	// Unknown id: 404.
	if rec := doAs(router, orgA, httptest.NewRequest(http.MethodGet, "/api/episodes/ep_nonesuch/pipeline", nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id status = %d, want 404", rec.Code)
	}

	// Owner, no StageRuns reader wired: 200, status-derived, nothing invented.
	rec := doAs(router, orgA, httptest.NewRequest(http.MethodGet, "/api/episodes/"+epID+"/pipeline", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := top["stages"]; !ok || len(top) != 1 {
		t.Fatalf("top-level keys = %v, want exactly [stages] (queued/total absent without runs)", top)
	}
	var body struct {
		Stages []map[string]json.RawMessage `json:"stages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode stages: %v", err)
	}
	if len(body.Stages) == 0 {
		t.Fatal("no stages in response")
	}
	for _, s := range body.Stages {
		for k := range s {
			switch k {
			case "name", "status", "duration_ms", "engine", "cost_cents":
			default:
				t.Errorf("unexpected stage key %q — the DTO contract is closed (engine detail must never appear)", k)
			}
		}
	}
}

// TestPipelineEndpointWithRuns drives the route with a canned provenance
// reader and asserts the enriched wire shape, including that only the PUBLIC
// engine label appears anywhere in the payload.
func TestPipelineEndpointWithRuns(t *testing.T) {
	repo := newFakeRepo()
	local, err := blob.NewLocal(t.TempDir(), []byte("test-secret"), func() time.Time { return time.Unix(1_700_000_000, 0) })
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	reader := &fakeStageRuns{runs: map[string][]StageRun{}}
	router := NewRouter(Deps{
		Authenticator:  stubAuth{},
		Directory:      stubDir{},
		Codec:          auth.NewCodec("test-secret"),
		Logger:         discard(),
		Now:            func() time.Time { return time.Unix(1_700_000_000, 0) },
		Episodes:       repo,
		Blob:           local,
		StageRuns:      reader,
		PipelineStages: fourStageChain,
	})

	runsID := seedEpisodeAs(t, repo, orgA)
	repo.setStatus(runsID, "ready", "k/proxies/p.mp4")
	setStage(repo, runsID, "moments")
	reader.runs[runsID] = []StageRun{
		okRun("ingest", 0, 1500, "bs-media-1", nil),
		okRun("transcribe", 1500, 62_000, "bs-asr-2", intPtr(4)),
		okRun("diarize", 63_500, 9000, "bs-lm-1", intPtr(2)),
		okRun("moments", 72_500, 8000, "bs-lm-1", intPtr(1)),
	}

	rec := doAs(router, orgA, httptest.NewRequest(http.MethodGet, "/api/episodes/"+runsID+"/pipeline", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var dto pipelineDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	tr := stageByName(t, dto, "transcribe")
	if tr.Status != "done" || tr.Engine != "bs-asr-2" || tr.DurationMs == nil || *tr.DurationMs != 62_000 || tr.CostCents == nil || *tr.CostCents != 4 {
		t.Errorf("transcribe = %+v, want done/bs-asr-2/62000/4", tr)
	}
	if dto.TotalMs == nil || *dto.TotalMs != 80_500 {
		t.Errorf("total = %v, want 80500", dto.TotalMs)
	}
	if dto.QueuedMs == nil {
		t.Errorf("queued = nil, want present (first run start - created_at)")
	}
}
