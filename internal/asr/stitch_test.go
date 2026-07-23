package asr

import (
	"errors"
	"testing"
)

// chunkTr builds a small valid single-segment chunk transcript with two words,
// using chunk-relative timings.
func chunkTr(text string, s0, e0, s1, e1 int) Transcript {
	return Transcript{
		Engine:   "bs-asr-1",
		Language: "fa",
		Segments: []Segment{{
			Idx: 0, StartMs: s0, EndMs: e1, Text: text,
			Words: []Word{
				{Text: "a", StartMs: s0, EndMs: e0, Conf: 0.9},
				{Text: "b", StartMs: s1, EndMs: e1, Conf: 0.8},
			},
		}},
	}
}

func TestStitchOffsetsAndIdx(t *testing.T) {
	chunks := []ChunkResult{
		{StartMs: 0, Transcript: chunkTr("one", 0, 400, 500, 900)},
		{StartMs: 900_000, Transcript: chunkTr("two", 100, 500, 600, 1000)},
		{StartMs: 1_800_000, Transcript: chunkTr("three", 0, 300, 400, 800)},
	}
	tr, err := StitchTranscripts("bs-asr-1", "fa", chunks)
	if err != nil {
		t.Fatalf("StitchTranscripts: %v", err)
	}
	if tr.Engine != "bs-asr-1" || tr.Language != "fa" {
		t.Errorf("engine/language = %q/%q", tr.Engine, tr.Language)
	}
	if len(tr.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(tr.Segments))
	}
	// Idx renumbered globally in time order.
	for i, s := range tr.Segments {
		if s.Idx != i {
			t.Errorf("segment %d has Idx %d", i, s.Idx)
		}
	}
	// Second chunk's timings shifted by 900_000ms.
	s1 := tr.Segments[1]
	if s1.StartMs != 900_100 || s1.EndMs != 901_000 {
		t.Errorf("segment 1 = [%d,%d], want [900100,901000]", s1.StartMs, s1.EndMs)
	}
	if s1.Words[0].StartMs != 900_100 || s1.Words[1].EndMs != 901_000 {
		t.Errorf("segment 1 words shifted wrong: %+v", s1.Words)
	}
	// Third chunk.
	if tr.Segments[2].StartMs != 1_800_000 {
		t.Errorf("segment 2 start = %d, want 1800000", tr.Segments[2].StartMs)
	}
	// The whole stitched transcript satisfies the boundary invariants.
	if err := tr.Validate(); err != nil {
		t.Fatalf("stitched transcript failed Validate: %v", err)
	}
}

func TestStitchReordersByStartMs(t *testing.T) {
	// Chunks supplied out of order are stitched in source-time order.
	chunks := []ChunkResult{
		{StartMs: 1_800_000, Transcript: chunkTr("c", 0, 100, 200, 300)},
		{StartMs: 0, Transcript: chunkTr("a", 0, 100, 200, 300)},
		{StartMs: 900_000, Transcript: chunkTr("b", 0, 100, 200, 300)},
	}
	tr, err := StitchTranscripts("bs-asr-1", "fa", chunks)
	if err != nil {
		t.Fatalf("StitchTranscripts: %v", err)
	}
	if tr.Segments[0].Text != "a" || tr.Segments[1].Text != "b" || tr.Segments[2].Text != "c" {
		t.Errorf("segments not in source-time order: %q %q %q",
			tr.Segments[0].Text, tr.Segments[1].Text, tr.Segments[2].Text)
	}
}

func TestStitchRejectsNonIncreasingStart(t *testing.T) {
	chunks := []ChunkResult{
		{StartMs: 0, Transcript: chunkTr("a", 0, 100, 200, 300)},
		{StartMs: 0, Transcript: chunkTr("b", 0, 100, 200, 300)}, // duplicate offset
	}
	_, err := StitchTranscripts("bs-asr-1", "fa", chunks)
	if !errors.Is(err, ErrInvalidTranscript) {
		t.Fatalf("err = %v, want ErrInvalidTranscript for duplicate offset", err)
	}
}

func TestStitchRejectsOverlap(t *testing.T) {
	// Chunk 0 spans [0,900_100) in source time but chunk 1 starts at 900_000,
	// so the shifted segments overlap -> Validate rejects.
	chunks := []ChunkResult{
		{StartMs: 0, Transcript: chunkTr("a", 0, 400, 500, 900_100)},
		{StartMs: 900_000, Transcript: chunkTr("b", 0, 100, 200, 300)},
	}
	_, err := StitchTranscripts("bs-asr-1", "fa", chunks)
	if !errors.Is(err, ErrInvalidTranscript) {
		t.Fatalf("err = %v, want ErrInvalidTranscript for overlapping chunks", err)
	}
}

func TestStitchNegativeOffset(t *testing.T) {
	chunks := []ChunkResult{{StartMs: -1, Transcript: chunkTr("a", 0, 100, 200, 300)}}
	if _, err := StitchTranscripts("bs-asr-1", "fa", chunks); !errors.Is(err, ErrInvalidTranscript) {
		t.Fatalf("err = %v, want ErrInvalidTranscript for negative offset", err)
	}
}

func TestStitchEmpty(t *testing.T) {
	tr, err := StitchTranscripts("bs-asr-1", "fa", nil)
	if err != nil {
		t.Fatalf("StitchTranscripts(nil): %v", err)
	}
	if len(tr.Segments) != 0 {
		t.Errorf("segments = %d, want 0", len(tr.Segments))
	}
}

func TestPlanChunks(t *testing.T) {
	// 44 min at 15-min chunks -> three windows, last one short.
	got, err := PlanChunks(2_640_000, 900_000)
	if err != nil {
		t.Fatalf("PlanChunks: %v", err)
	}
	want := [][2]int{{0, 900_000}, {900_000, 1_800_000}, {1_800_000, 2_640_000}}
	if len(got) != len(want) {
		t.Fatalf("windows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("window %d = %v, want %v", i, got[i], want[i])
		}
	}
	// Exact multiple -> no trailing short window.
	exact, _ := PlanChunks(1_800_000, 900_000)
	if len(exact) != 2 || exact[1] != [2]int{900_000, 1_800_000} {
		t.Errorf("exact-multiple plan = %v", exact)
	}
	// Windows tile the whole duration with no gap/overlap.
	prevEnd := 0
	for _, w := range got {
		if w[0] != prevEnd {
			t.Errorf("gap/overlap at %v (prev end %d)", w, prevEnd)
		}
		prevEnd = w[1]
	}
	if prevEnd != 2_640_000 {
		t.Errorf("windows end at %d, want 2640000", prevEnd)
	}
}

func TestPlanChunksRejectsNonPositive(t *testing.T) {
	if _, err := PlanChunks(0, 900_000); err == nil {
		t.Error("PlanChunks(0, ...) = nil err, want rejection")
	}
	if _, err := PlanChunks(1000, 0); err == nil {
		t.Error("PlanChunks(..., 0) = nil err, want rejection")
	}
}
