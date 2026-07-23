package pipeline

import (
	"context"
	"errors"
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

// fakeSegments captures the segments handed to ReplaceSegments so a test can
// assert the exact persisted rows. It can be scripted to fail.
type fakeSegments struct {
	mu    sync.Mutex
	byEp  map[string][]asr.Segment
	calls int
	err   error
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
