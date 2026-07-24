package pipeline

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"blueshift/internal/asr"
	"blueshift/internal/blob"
	"blueshift/internal/lang"

	// Register the Persian language so the LangEngineResolver test resolves its
	// asr engine slot from the real registry.
	_ "blueshift/internal/lang/fa"
)

// twoStageActive returns the [ingest, transcribe] active chain the transcribe
// stage tests run under. Transcribe is registered but out of the default
// (ingest-only) active chain, so these tests activate it explicitly; transcribe
// is the chain's terminal stage, claimed as a continuation from ingest.
func twoStageActive() []stageDef {
	return mustResolveActiveStages([]Stage{StageIngest, StageTranscribe})
}

// --- transcribe test doubles -------------------------------------------------

// fakeASR returns a fixed engine (or a scripted error) regardless of language, so
// a transcribe test drives the stage without the lang registry or a real
// provider. The real LangEngineResolver is covered separately.
type fakeASR struct {
	engine asr.Engine
	err    error
}

func (f fakeASR) EngineFor(context.Context, string) (asr.Engine, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.engine, nil
}

// scriptedEngine is an asr.Engine that returns a preset transcript per AudioKey
// (chunk-relative timings), or a fixed error. It lets a test control exactly what
// each chunk transcribes so the stitch/offset arithmetic is asserted precisely.
type scriptedEngine struct {
	label string
	byKey map[string]asr.Transcript
	err   error
}

func (e scriptedEngine) Label() string { return e.label }

func (e scriptedEngine) Transcribe(_ context.Context, req asr.TranscribeRequest) (asr.Transcript, error) {
	if e.err != nil {
		return asr.Transcript{}, e.err
	}
	tr, ok := e.byKey[req.AudioKey]
	if !ok {
		return asr.Transcript{}, errors.New("scriptedEngine: no transcript for key " + req.AudioKey)
	}
	return tr, nil
}

// countingEngine is an asr.Engine that returns one fixed, valid transcript and
// counts how many times Transcribe was called — the "billable call counter" the
// cost-safety idempotency tests assert stays put (delta 0) on a second, skipped run.
type countingEngine struct {
	label string
	tr    asr.Transcript
	mu    sync.Mutex
	calls int
}

func (e *countingEngine) Label() string { return e.label }

func (e *countingEngine) Transcribe(ctx context.Context, _ asr.TranscribeRequest) (asr.Transcript, error) {
	if err := ctx.Err(); err != nil {
		return asr.Transcript{}, err
	}
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return asr.Transcript{Engine: e.label, Language: e.tr.Language, Segments: cloneSegments(e.tr.Segments)}, nil
}

func (e *countingEngine) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// oneSegmentTranscript is a minimal valid fa transcript the countingEngine replays.
func oneSegmentTranscript() asr.Transcript {
	return asr.Transcript{Language: "fa", Segments: []asr.Segment{
		{Idx: 0, StartMs: 0, EndMs: 500, Text: "سلام", Words: []asr.Word{{Text: "سلام", StartMs: 0, EndMs: 500, Conf: 0.9}}},
	}}
}

// fakeSegments captures the segments handed to ReplaceSegments so a test can
// assert the exact persisted rows. It can be scripted to fail.
type fakeSegments struct {
	mu     sync.Mutex
	byEp   map[string][]asr.Segment
	calls  int
	err    error
	hasErr error // scripted error for HasSegments (cost-safety idempotency probe)
}

func newFakeSegments() *fakeSegments { return &fakeSegments{byEp: map[string][]asr.Segment{}} }

func (f *fakeSegments) ReplaceSegments(_ context.Context, _, episodePublicID string, segs []asr.Segment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.byEp[episodePublicID] = cloneSegments(segs)
	return nil
}

func (f *fakeSegments) get(epID string) []asr.Segment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byEp[epID]
}

// HasSegments is the cost-safety idempotency probe: an episode "already has
// segments" once ReplaceSegments has persisted a non-empty set (or a test seeded
// one). A hasErr can be scripted to exercise the stage's error path.
func (f *fakeSegments) HasSegments(_ context.Context, _, episodePublicID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hasErr != nil {
		return false, f.hasErr
	}
	return len(f.byEp[episodePublicID]) > 0, nil
}

// seed pre-populates an episode's transcript without going through the billable
// stage, so a test can start from an "already transcribed" state.
func (f *fakeSegments) seed(epID string, segs []asr.Segment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byEp[epID] = cloneSegments(segs)
}

func cloneSegments(segs []asr.Segment) []asr.Segment {
	out := make([]asr.Segment, len(segs))
	for i, s := range segs {
		out[i] = s
		if s.Words != nil {
			w := make([]asr.Word, len(s.Words))
			copy(w, s.Words)
			out[i].Words = w
		}
	}
	return out
}

// --- tests -------------------------------------------------------------------

// zwnj is the U+200C zero-width non-joiner, written as an escape so no literal
// format character sits in the source. zwnjWord and zwnjText embed it between the
// morphemes of "khosh-haalam"; the transcribe stage must persist it byte-for-byte
// (verbatim invariant): no normalization at rest.
const (
	zwnj     = "\u200c"
	zwnjWord = "خوش" + zwnj + "حالم"
	zwnjText = "خیلی " + zwnjWord
)

// TestTranscribeSingleChunkVerbatim drives the whole-audio (one-chunk) path with
// the REAL asr.FakeEngine and asserts the persisted segments are the fixture's
// exactly — including the U+200C ZWNJ byte-for-byte — and that the terminal
// finalize marks the episode ready while preserving ingest's proxy + duration.
func TestTranscribeSingleChunkVerbatim(t *testing.T) {
	fixture := `{
	  "language": "fa",
	  "raw": {"source": "fake"},
	  "segments": [
	    {"idx":0,"start_ms":0,"end_ms":900,"text":"سلام","words":[{"text":"سلام","start_ms":0,"end_ms":520,"conf":0.98}]},
	    {"idx":1,"start_ms":1000,"end_ms":1600,"text":"` + zwnjText + `","words":[
	       {"text":"خیلی","start_ms":1000,"end_ms":1200,"conf":0.96},
	       {"text":"` + zwnjWord + `","start_ms":1240,"end_ms":1600,"conf":0.95}
	    ]}
	  ]
	}`
	engine, err := asr.NewFakeEngine("bs-asr-1", fstest.MapFS{"fa.json": {Data: []byte(fixture)}})
	if err != nil {
		t.Fatalf("NewFakeEngine: %v", err)
	}

	repo := newFakeRepo()
	// Seed the episode already handed off from ingest (processing at ingest), with
	// the proxy + measured duration ingest recorded — the state transcribe claims.
	repo.addAtStage(epA, orgA, "ingest", "fa", 90_000)
	segs := newFakeSegments()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2},
		ASR:      fakeASR{engine: engine},
		Segments: segs,
		// Transcribe is out of the default (ingest-only) active chain now, so wire
		// the [ingest, transcribe] chain explicitly — transcribe is its terminal
		// stage, claimed as a continuation from processing-at-ingest.
		stages: twoStageActive(),
	}

	if err := r.Run(context.Background(), epA, "transcribe"); err != nil {
		t.Fatalf("Run(transcribe): %v", err)
	}

	// Terminal: ready, at transcribe, with ingest's outputs preserved (COALESCE).
	e := repo.get(epA)
	if e.status != "ready" || e.stage != "transcribe" {
		t.Errorf("state = (%q,%q), want (ready,transcribe)", e.status, e.stage)
	}
	wantProxy := orgA + "/" + epA + "/proxies/" + proxyFilename
	if e.proxyKey != wantProxy || e.durationMs != 90_000 {
		t.Errorf("outputs = (%q,%d), want ingest's proxy %q + duration 90000 preserved", e.proxyKey, e.durationMs, wantProxy)
	}

	got := segs.get(epA)
	if len(got) != 2 {
		t.Fatalf("persisted %d segments, want 2", len(got))
	}
	// Verbatim: the ZWNJ segment text and word text survive byte-for-byte.
	if got[1].Text != zwnjText {
		t.Errorf("segment text = %q, want the exact ZWNJ text %q", got[1].Text, zwnjText)
	}
	if !strings.Contains(got[1].Text, "\u200c") {
		t.Error("segment text lost its U+200C ZWNJ")
	}
	if len(got[1].Words) != 2 || got[1].Words[1].Text != "خوش\u200cحالم" {
		t.Errorf("word[1] = %+v, want the exact ZWNJ word", got[1].Words)
	}
	// idx is contiguous and ordered; timings are the fixture's (single chunk, no
	// offset).
	if got[0].Idx != 0 || got[1].Idx != 1 {
		t.Errorf("idx = %d,%d, want 0,1", got[0].Idx, got[1].Idx)
	}
	if got[0].StartMs != 0 || got[1].StartMs != 1000 {
		t.Errorf("segment starts = %d,%d, want 0,1000 (verbatim from ASR)", got[0].StartMs, got[1].StartMs)
	}
}

// TestTranscribeMultiChunkOffsets forces multi-chunk splitting (a tiny chunk cap
// over a longer duration) and proves the stage cuts the planned windows with
// ffmpeg, transcribes each chunk key, and stitches the chunk-relative timings
// into globally-offset, contiguously-indexed segments — the chunk-offset
// arithmetic the long-audio path relies on.
func TestTranscribeMultiChunkOffsets(t *testing.T) {
	// One chunk-relative segment per chunk. Each spans [0,400) within its chunk.
	chunkTranscript := func() asr.Transcript {
		return asr.Transcript{
			Engine: "bs-asr-1", Language: "fa",
			Segments: []asr.Segment{{
				Idx: 0, StartMs: 0, EndMs: 400, Text: "seg",
				Words: []asr.Word{{Text: "seg", StartMs: 0, EndMs: 400, Conf: 0.9}},
			}},
		}
	}
	// totalMs 2500, chunk 1000 -> windows [0,1000),[1000,2000),[2000,2500).
	starts := []int{0, 1000, 2000}
	byKey := map[string]asr.Transcript{}
	for _, s := range starts {
		key, err := blob.ProxyKey(orgA, epA, chunkFilename(s))
		if err != nil {
			t.Fatalf("chunk key: %v", err)
		}
		byKey[key] = chunkTranscript()
	}
	engine := scriptedEngine{label: "bs-asr-1", byKey: byKey}

	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "ingest", "fa", 2500)
	segs := newFakeSegments()
	md := &fakeMedia{}
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: md, Log: discard(),
		Config:   Config{Retries: 2, TranscribeChunkMs: 1000},
		ASR:      fakeASR{engine: engine},
		Segments: segs,
		// Transcribe is out of the default (ingest-only) active chain now, so wire
		// the [ingest, transcribe] chain explicitly — transcribe is its terminal
		// stage, claimed as a continuation from processing-at-ingest.
		stages: twoStageActive(),
	}

	if err := r.Run(context.Background(), epA, "transcribe"); err != nil {
		t.Fatalf("Run(transcribe): %v", err)
	}
	if e := repo.get(epA); e.status != "ready" || e.stage != "transcribe" {
		t.Fatalf("state = (%q,%q), want (ready,transcribe)", e.status, e.stage)
	}

	// The three windows were cut with the correct [start,duration] pairs.
	wantCuts := [][2]int{{0, 1000}, {1000, 1000}, {2000, 500}}
	if len(md.cuts) != len(wantCuts) {
		t.Fatalf("cuts = %v, want %v", md.cuts, wantCuts)
	}
	for i, c := range wantCuts {
		if md.cuts[i] != c {
			t.Errorf("cut[%d] = %v, want %v", i, md.cuts[i], c)
		}
	}

	// Each chunk contributed one segment; after the stitch their timings are shifted
	// by the chunk start and idx is renumbered globally.
	got := segs.get(epA)
	if len(got) != 3 {
		t.Fatalf("persisted %d segments, want 3", len(got))
	}
	for i, wantStart := range starts {
		if got[i].Idx != i {
			t.Errorf("segment %d idx = %d, want %d (renumbered in time order)", i, got[i].Idx, i)
		}
		if got[i].StartMs != wantStart || got[i].EndMs != wantStart+400 {
			t.Errorf("segment %d span = [%d,%d], want [%d,%d] (chunk offset applied)", i, got[i].StartMs, got[i].EndMs, wantStart, wantStart+400)
		}
		if len(got[i].Words) != 1 || got[i].Words[0].StartMs != wantStart {
			t.Errorf("segment %d word start = %v, want %d (word offset applied)", i, got[i].Words, wantStart)
		}
	}
}

// flatSegWords flattens segments' words in order — the sequence the verbatim
// invariant says resegmentation must preserve exactly.
func flatSegWords(segs []asr.Segment) []asr.Word {
	var out []asr.Word
	for _, s := range segs {
		out = append(out, s.Words...)
	}
	return out
}

// TestTranscribeResegmentsProviderMegaSegment drives the stage path over the
// embedded fa_long_take recording — ONE provider segment of 96 words, the
// 2026-07-24 prod shape where a whole take came back as a single wall of text —
// via its exact audio key, and proves the PERSISTED transcript is the
// resegmented view: multiple bounded, timed turns whose flattened words are the
// recording's byte-for-byte (verbatim: segmentation only regroups), with
// contiguous idx. The two-turn demo fixture stays covered by
// TestTwoStageAutoAdvanceTranscribesThenReDriveBillsZero, which still persists
// exactly its 2 (already readable, untouched) segments.
func TestTranscribeResegmentsProviderMegaSegment(t *testing.T) {
	engine, err := asr.NewDefaultFakeEngine("bs-asr-1")
	if err != nil {
		t.Fatalf("NewDefaultFakeEngine: %v", err)
	}
	const (
		orgDemo = "org_demo"
		epLong  = "ep_demo_long" // matches the fixture's exact audio key
	)
	audioKey, err := blob.ProxyKey(orgDemo, epLong, audioFilename)
	if err != nil {
		t.Fatalf("ProxyKey: %v", err)
	}
	// The recording as the engine returns it: one mega-segment.
	raw, err := engine.Transcribe(context.Background(), asr.TranscribeRequest{AudioKey: audioKey, Language: "fa"})
	if err != nil {
		t.Fatalf("Transcribe fixture: %v", err)
	}
	if len(raw.Segments) != 1 {
		t.Fatalf("fa_long_take recording has %d segments, want 1 (a mega-segment needing resegmentation)", len(raw.Segments))
	}

	repo := newFakeRepo()
	repo.addAtStage(epLong, orgDemo, "ingest", "fa", 40_000)
	segs := newFakeSegments()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2},
		ASR:      fakeASR{engine: engine},
		Segments: segs,
		stages:   twoStageActive(),
	}
	if err := r.Run(context.Background(), epLong, "transcribe"); err != nil {
		t.Fatalf("Run(transcribe): %v", err)
	}

	got := segs.get(epLong)
	if len(got) < 2 {
		t.Fatalf("persisted %d segments; the mega recording must be split into multiple timed turns", len(got))
	}
	// Verbatim: the flattened word sequence (text bytes incl. any U+200C,
	// timings, confidence, order) is the recording's exactly.
	if !reflect.DeepEqual(flatSegWords(got), flatSegWords(raw.Segments)) {
		t.Error("persisted words differ from the recording; resegmentation must only regroup")
	}
	for i, s := range got {
		if s.Idx != i {
			t.Errorf("segment %d Idx = %d, want %d (resequenced)", i, s.Idx, i)
		}
		if len(s.Words) == 0 {
			t.Fatalf("segment %d persisted empty", i)
		}
		if len(s.Words) > asr.DefaultResegmentMaxWords {
			t.Errorf("segment %d carries %d words, max %d", i, len(s.Words), asr.DefaultResegmentMaxWords)
		}
		if len(s.Words) > 1 && s.EndMs-s.StartMs > asr.DefaultResegmentMaxDurationMs {
			t.Errorf("segment %d spans %dms, max %d", i, s.EndMs-s.StartMs, asr.DefaultResegmentMaxDurationMs)
		}
		if s.StartMs != s.Words[0].StartMs || s.EndMs != s.Words[len(s.Words)-1].EndMs {
			t.Errorf("segment %d bounds [%d,%d] are not its first/last word times", i, s.StartMs, s.EndMs)
		}
	}
}

// TestTranscribeSegmentationConfigApplied proves the stage passes the
// SEGMENT_* thresholds through to the resegmenter: a tight MaxWords forces the
// standard two-turn fixture (5+5 words) into pieces of at most that many words,
// while the words themselves stay verbatim.
func TestTranscribeSegmentationConfigApplied(t *testing.T) {
	engine, err := asr.NewDefaultFakeEngine("bs-asr-1")
	if err != nil {
		t.Fatalf("NewDefaultFakeEngine: %v", err)
	}
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "ingest", "fa", 90_000)
	segs := newFakeSegments()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2, SegmentMaxWords: 2},
		ASR:      fakeASR{engine: engine},
		Segments: segs,
		stages:   twoStageActive(),
	}
	if err := r.Run(context.Background(), epA, "transcribe"); err != nil {
		t.Fatalf("Run(transcribe): %v", err)
	}
	got := segs.get(epA)
	if len(got) <= 2 {
		t.Fatalf("persisted %d segments, want > 2 (SegmentMaxWords=2 must split the 5-word turns)", len(got))
	}
	total := 0
	for i, s := range got {
		if len(s.Words) == 0 || len(s.Words) > 2 {
			t.Errorf("segment %d carries %d words, want 1-2 (configured cap)", i, len(s.Words))
		}
		total += len(s.Words)
	}
	if total != 10 {
		t.Errorf("persisted %d words in total, want 10 (verbatim regroup)", total)
	}
}

// TestTranscribeEngineFailureMarksFailedNeutral proves a persistent engine error
// exhausts the retries and fails the stage with a neutral error_id — no provider
// text leaks — and that no segments are persisted on a failed run.
func TestTranscribeEngineFailureMarksFailedNeutral(t *testing.T) {
	engine := scriptedEngine{label: "bs-asr-1", err: errors.New("provider exploded: chirp said no")}
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "ingest", "fa", 5000)
	segs := newFakeSegments()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2},
		ASR:      fakeASR{engine: engine},
		Segments: segs,
		// Transcribe is out of the default (ingest-only) active chain now, so wire
		// the [ingest, transcribe] chain explicitly — transcribe is its terminal
		// stage, claimed as a continuation from processing-at-ingest.
		stages: twoStageActive(),
	}

	err := r.Run(context.Background(), epA, "transcribe")
	if !errors.Is(err, ErrStageFailed) {
		t.Fatalf("Run err = %v, want ErrStageFailed", err)
	}
	e := repo.get(epA)
	if e.status != "failed" || e.stage != "transcribe" {
		t.Errorf("state = (%q,%q), want (failed,transcribe)", e.status, e.stage)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(e.errorID) {
		t.Errorf("error_id = %q, want a neutral 16-hex id", e.errorID)
	}
	// The returned error carries only the neutral id — never the provider cause.
	if !regexp.MustCompile(`error_id=[0-9a-f]{16}`).MatchString(err.Error()) {
		t.Errorf("returned error = %q, want a neutral error_id", err.Error())
	}
	for _, leak := range []string{"chirp", "provider", "exploded"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("returned error %q leaked %q", err.Error(), leak)
		}
	}
	if segs.calls != 0 {
		t.Errorf("ReplaceSegments called %d times on a failed run, want 0", segs.calls)
	}
}

// TestTranscribeNoDurationFails proves the stage refuses to run when ingest has
// not recorded a measured duration (an out-of-order run) rather than guessing an
// audio length — the verbatim invariant at the planning boundary.
func TestTranscribeNoDurationFails(t *testing.T) {
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "ingest", "fa", 0) // no measured duration
	segs := newFakeSegments()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2},
		ASR:      fakeASR{engine: scriptedEngine{label: "bs-asr-1", byKey: map[string]asr.Transcript{}}},
		Segments: segs,
		stages:   twoStageActive(),
	}
	if err := r.Run(context.Background(), epA, "transcribe"); !errors.Is(err, ErrStageFailed) {
		t.Fatalf("Run err = %v, want ErrStageFailed (no measured duration)", err)
	}
	if segs.calls != 0 {
		t.Errorf("ReplaceSegments called %d times, want 0", segs.calls)
	}
}

// TestTranscribeStageReDriveBillsZero is the transcribe cost-safety idempotency
// proof: a plain re-drive of an already-transcribed episode makes ZERO billable ASR
// calls. The first drive transcribes once and persists; re-seeding at ingest and
// re-running SKIPS the paid engine entirely (the engine call counter and the persist
// counter both stay put, and process_attempts does NOT advance) while the episode
// still finalizes ready (CLAUDE.md "Billable-service cost safety").
func TestTranscribeStageReDriveBillsZero(t *testing.T) {
	engine := &countingEngine{label: "bs-asr-1", tr: oneSegmentTranscript()}
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "ingest", "fa", 90_000)
	segs := newFakeSegments()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2},
		ASR:      fakeASR{engine: engine},
		Segments: segs,
		stages:   twoStageActive(),
	}

	// Drive #1: the paid ASR call runs exactly once and the transcript is persisted.
	if err := r.Run(context.Background(), epA, "transcribe"); err != nil {
		t.Fatalf("Run(transcribe) #1: %v", err)
	}
	if engine.callCount() != 1 || segs.calls != 1 {
		t.Fatalf("after drive #1: engine calls=%d, persist calls=%d, want 1 and 1", engine.callCount(), segs.calls)
	}
	billedAfterFirst := repo.get(epA).processAttempts
	if billedAfterFirst != 1 {
		t.Fatalf("process_attempts after drive #1 = %d, want 1 (one billable attempt)", billedAfterFirst)
	}

	// Drive #2 (a plain re-drive): re-seed at ingest and re-run. The stage must SKIP
	// the billable ASR call — no engine call, no persist, no attempt consumed.
	repo.addAtStage(epA, orgA, "ingest", "fa", 90_000)
	repo.setProcessAttempts(epA, billedAfterFirst) // addAtStage reset the fresh fakeEp
	if err := r.Run(context.Background(), epA, "transcribe"); err != nil {
		t.Fatalf("Run(transcribe) #2: %v", err)
	}
	if engine.callCount() != 1 {
		t.Errorf("engine calls after re-drive = %d, want 1 (ZERO billable calls on the second run)", engine.callCount())
	}
	if segs.calls != 1 {
		t.Errorf("ReplaceSegments calls after re-drive = %d, want 1 (the re-drive persisted nothing)", segs.calls)
	}
	if got := repo.get(epA).processAttempts; got != billedAfterFirst {
		t.Errorf("process_attempts after re-drive = %d, want %d (a skipped run consumes no billable budget)", got, billedAfterFirst)
	}
	if e := repo.get(epA); e.status != "ready" || e.stage != "transcribe" {
		t.Errorf("state after re-drive = (%q,%q), want (ready,transcribe) — a skip still finalizes", e.status, e.stage)
	}
}

// TestTwoStageAutoAdvanceTranscribesThenReDriveBillsZero is the end-to-end proof
// of the demo/e2e path the original re-enable regression missed: an UPLOADED
// episode (status 'uploaded') claimed at ingest AUTO-ADVANCES into transcribe and
// finalizes 'ready' WITH segments — driven by the REAL offline FakeEngine through
// the LangEngineResolver, exactly as make demo/e2e wire it (ASR_ENGINE_MODE=fake,
// PIPELINE_STAGES=ingest,transcribe). The whole chain runs from ONE Run(ingest)
// via a trigger that runs the next stage in-process, mirroring the exec trigger's
// env-inheriting cross-process spawn. It then proves cost-safety survives the
// two-stage flow: a plain re-drive of the already-transcribed episode makes ZERO
// further billable ASR calls (process_attempts and the persist count both stay
// put) while still finalizing 'ready'.
func TestTwoStageAutoAdvanceTranscribesThenReDriveBillsZero(t *testing.T) {
	engine, err := asr.NewDefaultFakeEngine("bs-asr-1")
	if err != nil {
		t.Fatalf("NewDefaultFakeEngine: %v", err)
	}
	reg, err := asr.NewRegistry(engine)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4") // 'uploaded', language fa
	segs := newFakeSegments()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2, AutoAdvance: true},
		ASR:      LangEngineResolver{Registry: reg, Label: "bs-asr-1"},
		Segments: segs,
	}
	if err := r.SetActiveStages([]Stage{StageIngest, StageTranscribe}); err != nil {
		t.Fatalf("SetActiveStages: %v", err)
	}
	tr := &runningTrigger{r: r}
	r.Trigger = tr

	// One Run(ingest) drives ingest -> (auto-advance) -> transcribe -> ready, the
	// whole chain in fake mode, just like an uploaded episode in make demo/e2e.
	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("Run(ingest): %v", err)
	}

	e := repo.get(epA)
	if e.status != "ready" || e.stage != "transcribe" {
		t.Fatalf("after chain: state = (%q,%q), want (ready,transcribe)", e.status, e.stage)
	}
	if got := segs.get(epA); len(got) != 2 { // the offline fa fixture is two segments
		t.Fatalf("persisted %d segments, want 2 (the fa fake fixture)", len(segs.get(epA)))
	}
	if calls := tr.snapshot(); len(calls) != 1 || calls[0] != [2]string{epA, "transcribe"} {
		t.Fatalf("auto-advance fired %v, want exactly [[%s transcribe]]", calls, epA)
	}
	billedAfterChain := e.processAttempts
	if billedAfterChain != 1 {
		t.Fatalf("process_attempts after chain = %d, want 1 (one billable transcribe attempt)", billedAfterChain)
	}
	if segs.calls != 1 {
		t.Fatalf("ReplaceSegments calls after chain = %d, want 1", segs.calls)
	}

	// Re-drive the transcribe stage: re-seed the episode at ingest (the state a
	// retry/re-drive claims from), preserving process_attempts. The persisted
	// segments already exist, so the idempotency guard SKIPS the billable call.
	repo.addAtStage(epA, orgA, "ingest", "fa", e.durationMs)
	repo.setProcessAttempts(epA, billedAfterChain) // addAtStage reset the fresh fakeEp
	if err := r.Run(context.Background(), epA, "transcribe"); err != nil {
		t.Fatalf("Run(transcribe) re-drive: %v", err)
	}
	if got := repo.get(epA).processAttempts; got != billedAfterChain {
		t.Errorf("process_attempts after re-drive = %d, want %d (a re-drive of a transcribed episode bills ZERO)", got, billedAfterChain)
	}
	if segs.calls != 1 {
		t.Errorf("ReplaceSegments calls after re-drive = %d, want 1 (the re-drive persisted nothing)", segs.calls)
	}
	if e := repo.get(epA); e.status != "ready" || e.stage != "transcribe" {
		t.Errorf("state after re-drive = (%q,%q), want (ready,transcribe) — a skip still finalizes", e.status, e.stage)
	}
}

// TestTranscribeStageReprocessReTranscribes proves the explicit reprocess override:
// with Config.Reprocess set, the stage IGNORES the idempotency skip and re-runs the
// paid ASR engine even though the episode already has segments — the deliberate
// operator re-process that a plain retry/re-drive must never trigger.
func TestTranscribeStageReprocessReTranscribes(t *testing.T) {
	engine := &countingEngine{label: "bs-asr-1", tr: oneSegmentTranscript()}
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "ingest", "fa", 90_000)
	segs := newFakeSegments()
	segs.seed(epA, oneSegmentTranscript().Segments) // already transcribed
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2, Reprocess: true},
		ASR:      fakeASR{engine: engine},
		Segments: segs,
		stages:   twoStageActive(),
	}
	if err := r.Run(context.Background(), epA, "transcribe"); err != nil {
		t.Fatalf("Run(transcribe) reprocess: %v", err)
	}
	if engine.callCount() != 1 {
		t.Errorf("engine calls = %d, want 1 (reprocess re-transcribes despite existing segments)", engine.callCount())
	}
	if got := repo.get(epA).processAttempts; got != 1 {
		t.Errorf("process_attempts = %d, want 1 (reprocess still consumes and respects the attempt budget)", got)
	}
}

// TestTranscribeStageAttemptCapBlocksBeforeBillableCall proves the per-episode cap:
// an episode already at MAX_PROCESS_ATTEMPTS hard-fails WITHOUT ever calling the ASR
// engine, with a neutral error_id and no increment past the cap. This is the runaway
// backstop — even a re-drive loop can never bill beyond the ceiling.
func TestTranscribeStageAttemptCapBlocksBeforeBillableCall(t *testing.T) {
	engine := &countingEngine{label: "bs-asr-1", tr: oneSegmentTranscript()}
	repo := newFakeRepo()
	repo.addAtStage(epA, orgA, "ingest", "fa", 90_000)
	repo.setProcessAttempts(epA, DefaultMaxProcessAttempts) // already at the cap
	segs := newFakeSegments()
	r := &Runner{
		Repo: repo, Blob: newRemoteBlob(t), Media: &fakeMedia{}, Log: discard(),
		Config:   Config{Retries: 2}, // MaxProcessAttempts unset -> DefaultMaxProcessAttempts
		ASR:      fakeASR{engine: engine},
		Segments: segs,
		stages:   twoStageActive(),
	}

	err := r.Run(context.Background(), epA, "transcribe")
	if !errors.Is(err, ErrStageFailed) {
		t.Fatalf("Run err = %v, want ErrStageFailed (attempt cap)", err)
	}
	e := repo.get(epA)
	if e.status != "failed" || e.stage != "transcribe" {
		t.Errorf("state = (%q,%q), want (failed,transcribe)", e.status, e.stage)
	}
	if engine.callCount() != 0 {
		t.Errorf("engine calls = %d, want 0 (the cap blocks BEFORE any billable call)", engine.callCount())
	}
	if segs.calls != 0 {
		t.Errorf("ReplaceSegments calls = %d, want 0 on a capped run", segs.calls)
	}
	if e.processAttempts != DefaultMaxProcessAttempts {
		t.Errorf("process_attempts = %d, want %d (unchanged — the cap does not increment)", e.processAttempts, DefaultMaxProcessAttempts)
	}
	// The client-visible error is neutral: an error_id only, never the cap detail.
	if !regexp.MustCompile(`error_id=[0-9a-f]{16}`).MatchString(err.Error()) {
		t.Errorf("returned error = %q, want a neutral error_id", err.Error())
	}
	if strings.Contains(err.Error(), "cap") || strings.Contains(err.Error(), "attempt") {
		t.Errorf("returned error %q leaked the cap detail; it must carry only a neutral id", err.Error())
	}
}

// TestLangEngineResolverResolvesByLangAndLabel exercises the real resolver: the
// lang registry gates the tag and declares its asr slot, and the neutral label
// selects the registered engine. Unknown languages and unbound labels are errors,
// never silent defaults.
func TestLangEngineResolverResolvesByLangAndLabel(t *testing.T) {
	engine, err := asr.NewFakeEngine("bs-asr-1", fstest.MapFS{
		"fa.json": {Data: []byte(`{"language":"fa","segments":[]}`)},
	})
	if err != nil {
		t.Fatalf("NewFakeEngine: %v", err)
	}
	reg, err := asr.NewRegistry(engine)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	res := LangEngineResolver{Registry: reg, Label: "bs-asr-1"}

	got, err := res.EngineFor(context.Background(), "fa")
	if err != nil {
		t.Fatalf("EngineFor(fa): %v", err)
	}
	if got.Label() != "bs-asr-1" {
		t.Errorf("resolved engine label = %q, want bs-asr-1", got.Label())
	}
	// fa-IR resolves to the registered fa (primary-subtag fallback in lang.Get).
	if _, err := res.EngineFor(context.Background(), "fa-IR"); err != nil {
		t.Errorf("EngineFor(fa-IR): %v, want the fa engine", err)
	}
	// An unregistered language is an explicit error.
	if _, err := res.EngineFor(context.Background(), "zz"); err == nil {
		t.Error("EngineFor(zz) = nil error, want unknown-language error")
	}
	// An unbound label is an explicit error.
	bad := LangEngineResolver{Registry: reg, Label: "bs-asr-9"}
	if _, err := bad.EngineFor(context.Background(), "fa"); !errors.Is(err, asr.ErrUnknownEngine) {
		t.Errorf("EngineFor with unbound label err = %v, want ErrUnknownEngine", err)
	}
}

// ensure lang is referenced (the fa import is for its registration side effect).
var _ = lang.EngineASR
