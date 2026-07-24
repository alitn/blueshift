package pipeline

// Runner-level stage-run provenance tests: the open-at-claim / close-at-
// finalize choreography, the failed close on the SIGTERM detached path (which
// must share — never stretch — the bounded finalize window), the best-effort
// contract (a broken recorder never fails a run), and the engine identity map.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"blueshift/internal/asr"
)

// fakeRuns is an in-memory RunRecorder. Like the real store, it can be told to
// honor context cancellation (respectsCtx), proving the shutdown close rides
// the detached bounded context, and to error (startErr/finishErr), proving the
// best-effort contract.
type fakeRuns struct {
	mu          sync.Mutex
	respectsCtx bool
	startErr    error
	finishErr   error
	nextID      int64
	starts      []fakeRunStart
	finishes    []fakeRunFinish
}

type fakeRunStart struct {
	org, ep, stage, label, detail string
	id                            int64
}

type fakeRunFinish struct {
	id  int64
	fin StageRunFinish
}

func (f *fakeRuns) StartStageRun(ctx context.Context, orgID, epID, stage, label, detail string) (int64, error) {
	if f.respectsCtx && ctx.Err() != nil {
		return 0, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return 0, f.startErr
	}
	f.nextID++
	f.starts = append(f.starts, fakeRunStart{org: orgID, ep: epID, stage: stage, label: label, detail: detail, id: f.nextID})
	return f.nextID, nil
}

func (f *fakeRuns) FinishStageRun(ctx context.Context, runID int64, fin StageRunFinish) error {
	if f.respectsCtx && ctx.Err() != nil {
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.finishErr != nil {
		return f.finishErr
	}
	f.finishes = append(f.finishes, fakeRunFinish{id: runID, fin: fin})
	return nil
}

func (f *fakeRuns) snapshot() ([]fakeRunStart, []fakeRunFinish) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeRunStart(nil), f.starts...), append([]fakeRunFinish(nil), f.finishes...)
}

// TestStageRunRecordedOnSuccess asserts a green ingest run opens one provenance
// row at claim (with the stage's engine identity) and closes it 'ok' after the
// terminal finalize.
func TestStageRunRecordedOnSuccess(t *testing.T) {
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	runs := &fakeRuns{}
	r := newRunner(repo, newRemoteBlob(t), &fakeMedia{}, Config{Retries: 2})
	r.Runs = runs
	r.Engines = map[Stage]StageEngine{
		StageIngest: {Label: "bs-media-1", Detail: "ffmpeg"},
	}

	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	starts, finishes := runs.snapshot()
	if len(starts) != 1 || len(finishes) != 1 {
		t.Fatalf("starts/finishes = %d/%d, want 1/1", len(starts), len(finishes))
	}
	s := starts[0]
	if s.org != orgA || s.ep != epA || s.stage != "ingest" || s.label != "bs-media-1" || s.detail != "ffmpeg" {
		t.Errorf("start = %+v, want the claimed episode + the ingest engine identity", s)
	}
	fin := finishes[0]
	if fin.id != s.id || fin.fin.Outcome != RunOutcomeOK {
		t.Errorf("finish = %+v, want id %d outcome ok", fin, s.id)
	}
	if e := repo.get(epA); e.status != "ready" {
		t.Errorf("status = %q, want ready", e.status)
	}
}

// TestStageRunRecordedOnFailure asserts an exhausted run closes its provenance
// row 'failed' after the mark-failed finalize.
func TestStageRunRecordedOnFailure(t *testing.T) {
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	runs := &fakeRuns{}
	boom := errors.New("render broke")
	md := &fakeMedia{renderErrs: []error{boom, boom, boom}}
	r := newRunner(repo, newRemoteBlob(t), md, Config{Retries: 2})
	r.Runs = runs

	if err := r.Run(context.Background(), epA, "ingest"); !errors.Is(err, ErrStageFailed) {
		t.Fatalf("Run err = %v, want ErrStageFailed", err)
	}
	starts, finishes := runs.snapshot()
	if len(starts) != 1 || len(finishes) != 1 {
		t.Fatalf("starts/finishes = %d/%d, want 1/1 (one row per RUN, not per attempt)", len(starts), len(finishes))
	}
	if finishes[0].fin.Outcome != RunOutcomeFailed {
		t.Errorf("outcome = %q, want failed", finishes[0].fin.Outcome)
	}
}

// TestStageRunShutdownClosesFailedOnDetachedContext mirrors
// TestRunShutdownMarksFailedBounded with a recorder that honors context
// cancellation: the failed provenance close must land on the DETACHED bounded
// finalize context (the run's own context is already dead), inside the same
// grace-window budget, and after the load-bearing mark-failed.
func TestStageRunShutdownClosesFailedOnDetachedContext(t *testing.T) {
	repo := newFakeRepo()
	repo.markRespectsCtx = true
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	runs := &fakeRuns{respectsCtx: true}
	md := &fakeMedia{blockOnCtx: true}
	r := newRunner(repo, newRemoteBlob(t), md, Config{StageTimeout: time.Minute, Retries: 2})
	r.Runs = runs

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, epA, "ingest") }()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, ErrStageFailed) {
			t.Fatalf("Run err = %v, want ErrStageFailed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s of shutdown; grace-window bound violated")
	}
	if e := repo.get(epA); e.status != "failed" {
		t.Fatalf("status = %q, want failed (the load-bearing finalize comes first)", e.status)
	}
	_, finishes := runs.snapshot()
	if len(finishes) != 1 || finishes[0].fin.Outcome != RunOutcomeFailed {
		t.Fatalf("finishes = %+v, want one failed close on the detached context", finishes)
	}
}

// TestStageRunRecorderErrorsNeverFailARun asserts the best-effort contract: a
// recorder that errors on open (and close) changes nothing about the run — the
// episode still completes and no Finish is attempted for an unopened row.
func TestStageRunRecorderErrorsNeverFailARun(t *testing.T) {
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	runs := &fakeRuns{startErr: errors.New("provenance db down")}
	r := newRunner(repo, newRemoteBlob(t), &fakeMedia{}, Config{Retries: 2})
	r.Runs = runs

	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("Run: %v (a provenance miss must never fail a run)", err)
	}
	if e := repo.get(epA); e.status != "ready" {
		t.Errorf("status = %q, want ready", e.status)
	}
	_, finishes := runs.snapshot()
	if len(finishes) != 0 {
		t.Errorf("finishes = %+v, want none (runID 0 is never closed)", finishes)
	}
}

// TestStageRunTranscribeFacts asserts the transcribe stage's provenance facts:
// provider segments in -> resegmented segments out, the billable counter as the
// attempt, the ASR duration-rate cost, and the resolved segmentation tunables
// as params.
func TestStageRunTranscribeFacts(t *testing.T) {
	fixture := `{
	  "language": "fa",
	  "segments": [
	    {"idx":0,"start_ms":0,"end_ms":900,"text":"سلام","words":[{"text":"سلام","start_ms":0,"end_ms":520,"conf":0.98}]}
	  ]
	}`
	engine, err := asr.NewFakeEngine("bs-asr-2", fstest.MapFS{"fa.json": {Data: []byte(fixture)}})
	if err != nil {
		t.Fatalf("NewFakeEngine: %v", err)
	}
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "ingest", "fa", 90_000) // 90s of audio
	runs := &fakeRuns{}
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		// 400 cents/hour x 90s = 10 cents exactly (integer ceil).
		Config:   Config{Retries: 2, ASRPriceCentsPerHour: 400, SegmentGapMs: 500},
		ASR:      fakeASR{engine: engine},
		Segments: newFakeSegments(),
		Runs:     runs,
		Engines:  map[Stage]StageEngine{StageTranscribe: {Label: "bs-asr-2", Detail: "model@loc"}},
		stages:   twoStageActive(),
	}
	if err := r.Run(context.Background(), epA, "transcribe"); err != nil {
		t.Fatalf("Run(transcribe): %v", err)
	}
	starts, finishes := runs.snapshot()
	if len(starts) != 1 || len(finishes) != 1 {
		t.Fatalf("starts/finishes = %d/%d, want 1/1", len(starts), len(finishes))
	}
	if starts[0].label != "bs-asr-2" || starts[0].detail != "model@loc" {
		t.Errorf("engine identity = %q/%q, want bs-asr-2/model@loc", starts[0].label, starts[0].detail)
	}
	facts := finishes[0].fin.Facts
	if facts.ItemsIn != 1 || facts.ItemsOut != 1 {
		t.Errorf("items = %d->%d, want 1->1", facts.ItemsIn, facts.ItemsOut)
	}
	if facts.Attempt != 1 {
		t.Errorf("attempt = %d, want 1 (the billable counter value)", facts.Attempt)
	}
	if facts.CostCents != 10 {
		t.Errorf("cost = %d, want 10 (90s at 400 cents/hour)", facts.CostCents)
	}
	want := `{"segment_gap_ms":500,"segment_max_duration_ms":30000,"segment_max_words":60}`
	if string(facts.Params) != want {
		t.Errorf("params = %s, want %s (config override + resolved defaults)", facts.Params, want)
	}
}

// TestASRCostCents pins the duration-rate arithmetic: integer cents, ceiling,
// and 0 (-> NULL) whenever the rate or the measurement is missing.
func TestASRCostCents(t *testing.T) {
	cases := []struct {
		durationMs int64
		rate, want int
	}{
		{3_600_000, 96, 96},  // exactly one hour
		{1_800_000, 96, 48},  // half an hour
		{90_000, 400, 10},    // 90s at 400/h
		{1, 96, 1},           // any metered sliver rounds UP, never to free
		{0, 96, 0},           // nothing measured -> no cost
		{3_600_000, 0, 0},    // no rate -> no cost
		{-5, 96, 0},          // defensive
		{7_200_000, 96, 192}, // two hours
	}
	for _, c := range cases {
		if got := asrCostCents(c.durationMs, c.rate); got != c.want {
			t.Errorf("asrCostCents(%d, %d) = %d, want %d", c.durationMs, c.rate, got, c.want)
		}
	}
}

// TestStageRunNotOpenedOnRefusedClaim asserts a losing/duplicate invocation —
// which must stay a clean no-op — opens no provenance row: recording sits
// strictly AFTER the claim compare-and-set.
func TestStageRunNotOpenedOnRefusedClaim(t *testing.T) {
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	runs := &fakeRuns{}
	r := newRunner(repo, newRemoteBlob(t), &fakeMedia{}, Config{Retries: 2})
	r.Runs = runs

	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("second Run (refused claim): %v", err)
	}
	starts, _ := runs.snapshot()
	if len(starts) != 1 {
		t.Errorf("starts = %d, want 1 (a refused claim records nothing)", len(starts))
	}
}
