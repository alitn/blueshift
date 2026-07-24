package asr

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// zwnj is U+200C (zero-width non-joiner), written as an escape so no literal
// format character sits in the source. Persian compounds carry it between
// morphemes; resegmentation must move the word bytes untouched.
const zwnj = "\u200c"

// w builds a word with the timing under test. Conf carries a distinct value so
// equality checks would catch a dropped or rebuilt field.
func w(text string, start, end int) Word {
	return Word{Text: text, StartMs: start, EndMs: end, Conf: 0.5}
}

// flatWords flattens a transcript's words in order — the sequence the verbatim
// invariant says Resegment must preserve exactly.
func flatWords(t Transcript) []Word {
	var out []Word
	for _, s := range t.Segments {
		out = append(out, s.Words...)
	}
	return out
}

// mustValidate fails the test if tr violates the boundary invariants.
func mustValidate(t *testing.T, tr Transcript, label string) {
	t.Helper()
	if err := tr.Validate(); err != nil {
		t.Fatalf("%s failed Validate: %v", label, err)
	}
}

// segFromWords assembles a valid single segment spanning its words.
func segFromWords(idx int, words ...Word) Segment {
	return Segment{
		Idx:     idx,
		StartMs: words[0].StartMs,
		EndMs:   words[len(words)-1].EndMs,
		Text:    joinWords(words),
		Words:   words,
	}
}

func TestResegmentSplitsAtPauseGapsOnly(t *testing.T) {
	// Gaps: 100, 700, 650, 800 — only the >=700 gaps split (default GapMs).
	seg := segFromWords(0,
		w("a", 0, 200),
		w("b", 300, 500),   // gap 100
		w("c", 1200, 1400), // gap 700: boundary
		w("d", 2050, 2250), // gap 650: no boundary
		w("e", 3050, 3250), // gap 800: boundary
	)
	in := Transcript{Engine: "bs-asr-1", Language: "fa", Segments: []Segment{seg}}
	mustValidate(t, in, "input")

	got := Resegment(in, ResegmentOptions{})
	mustValidate(t, got, "output")
	if len(got.Segments) != 3 {
		t.Fatalf("segments = %d, want 3 (split at the two >=700ms gaps)", len(got.Segments))
	}
	wantTexts := []string{"a b", "c d", "e"}
	wantSpans := [][2]int{{0, 500}, {1200, 2250}, {3050, 3250}}
	for i, s := range got.Segments {
		if s.Text != wantTexts[i] {
			t.Errorf("segment %d text = %q, want %q", i, s.Text, wantTexts[i])
		}
		if s.StartMs != wantSpans[i][0] || s.EndMs != wantSpans[i][1] {
			t.Errorf("segment %d span = [%d,%d], want [%d,%d] (first/last word times)",
				i, s.StartMs, s.EndMs, wantSpans[i][0], wantSpans[i][1])
		}
		if s.Idx != i {
			t.Errorf("segment %d Idx = %d, want %d (resequenced)", i, s.Idx, i)
		}
	}
	if !reflect.DeepEqual(flatWords(got), flatWords(in)) {
		t.Error("flattened words changed; resegmentation must only regroup")
	}
}

func TestResegmentEngineLanguageRawCarried(t *testing.T) {
	in := Transcript{
		Engine: "bs-asr-1", Language: "fa",
		Raw:      []byte(`{"k":"v"}`),
		Segments: []Segment{segFromWords(0, w("a", 0, 100))},
	}
	got := Resegment(in, ResegmentOptions{})
	if got.Engine != in.Engine || got.Language != in.Language || string(got.Raw) != string(in.Raw) {
		t.Errorf("envelope changed: got %q/%q/%s", got.Engine, got.Language, got.Raw)
	}
}

func TestResegmentUnsplitSegmentRetainedByteVerbatim(t *testing.T) {
	// Within all bounds, no >=700 gap. Provider text differs from the joined
	// words (punctuation) and the segment end is padded past the last word
	// (resultEndOffset behaviour) — BOTH must survive untouched.
	seg := Segment{
		Idx: 3, StartMs: 1000, EndMs: 2600, // padded end: last word ends 2400
		Text: "سلام، خوش" + zwnj + "حالم.",
		Words: []Word{
			w("سلام،", 1000, 1500),
			w("خوش"+zwnj+"حالم.", 1600, 2400),
		},
	}
	in := Transcript{Language: "fa", Segments: []Segment{seg}}
	mustValidate(t, in, "input")

	got := Resegment(in, ResegmentOptions{})
	mustValidate(t, got, "output")
	if len(got.Segments) != 1 {
		t.Fatalf("segments = %d, want 1 (nothing to split)", len(got.Segments))
	}
	out := got.Segments[0]
	if out.Text != seg.Text {
		t.Errorf("text = %q, want the provider text %q retained verbatim", out.Text, seg.Text)
	}
	if out.StartMs != 1000 || out.EndMs != 2600 {
		t.Errorf("span = [%d,%d], want the original padded [1000,2600]", out.StartMs, out.EndMs)
	}
	if !reflect.DeepEqual(out.Words, seg.Words) {
		t.Errorf("words changed on an untouched segment: %+v", out.Words)
	}
	if out.Idx != 0 {
		t.Errorf("Idx = %d, want 0 (resequenced even when unsplit)", out.Idx)
	}
}

func TestResegmentZWNJBytesSurviveSplit(t *testing.T) {
	// The ZWNJ word sits right after a pause boundary, so it crosses a split.
	seg := segFromWords(0,
		w("خیلی", 0, 300),
		w("ممنون", 350, 800),
		w("می"+zwnj+"خواهم", 1600, 2200), // gap 800: boundary before this word
		w("شروع", 2260, 2600),
		w("کنم", 2660, 2900),
	)
	in := Transcript{Language: "fa", Segments: []Segment{seg}}
	mustValidate(t, in, "input")

	got := Resegment(in, ResegmentOptions{})
	if len(got.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(got.Segments))
	}
	second := got.Segments[1]
	if second.Words[0].Text != "می"+zwnj+"خواهم" {
		t.Errorf("word text = %q, lost its U+200C", second.Words[0].Text)
	}
	if !strings.Contains(second.Text, zwnj) {
		t.Error("derived segment text lost the U+200C inside the word")
	}
	if want := "می" + zwnj + "خواهم شروع کنم"; second.Text != want {
		t.Errorf("derived text = %q, want %q (words joined by single spaces)", second.Text, want)
	}
}

func TestResegmentMaxWordsSplitsAtLargestGap(t *testing.T) {
	// 8 contiguous words, MaxWords 5. Gaps are all 50ms except one 300ms gap
	// after word 3 — under GapMs, but the largest available: the bounds split
	// must land THERE, not mid-phrase at a 50ms gap nearer the middle.
	words := []Word{
		w("w0", 0, 200),
		w("w1", 250, 450),
		w("w2", 500, 700),
		w("w3", 750, 950),
		w("w4", 1250, 1450), // gap 300 (largest)
		w("w5", 1500, 1700),
		w("w6", 1750, 1950),
		w("w7", 2000, 2200),
	}
	in := Transcript{Language: "fa", Segments: []Segment{segFromWords(0, words...)}}
	mustValidate(t, in, "input")

	got := Resegment(in, ResegmentOptions{GapMs: 700, MaxDurationMs: 30_000, MaxWords: 5})
	mustValidate(t, got, "output")
	if len(got.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(got.Segments))
	}
	if got.Segments[0].Text != "w0 w1 w2 w3" || got.Segments[1].Text != "w4 w5 w6 w7" {
		t.Errorf("split = %q | %q, want the boundary at the 300ms gap", got.Segments[0].Text, got.Segments[1].Text)
	}
}

func TestResegmentMaxDurationSplits(t *testing.T) {
	// 4 slow words spanning 44s with sub-GapMs gaps; MaxDurationMs 30s forces a
	// split at the largest gap (600ms, after word 1).
	words := []Word{
		w("w0", 0, 10_000),
		w("w1", 10_400, 21_000),
		w("w2", 21_600, 33_000), // gap 600 (largest)
		w("w3", 33_400, 44_000),
	}
	in := Transcript{Language: "fa", Segments: []Segment{segFromWords(0, words...)}}
	mustValidate(t, in, "input")

	got := Resegment(in, ResegmentOptions{})
	mustValidate(t, got, "output")
	if len(got.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(got.Segments))
	}
	if got.Segments[0].Text != "w0 w1" || got.Segments[1].Text != "w2 w3" {
		t.Errorf("split = %q | %q, want the boundary at the 600ms gap", got.Segments[0].Text, got.Segments[1].Text)
	}
	for i, s := range got.Segments {
		if s.EndMs-s.StartMs > DefaultResegmentMaxDurationMs {
			t.Errorf("segment %d spans %dms, want <= %d", i, s.EndMs-s.StartMs, DefaultResegmentMaxDurationMs)
		}
	}
}

func TestResegmentSingleOversizedWordKept(t *testing.T) {
	// One word longer than MaxDurationMs: no boundary exists inside a word, so
	// it stays — a timestamp is never invented.
	in := Transcript{Language: "fa", Segments: []Segment{
		segFromWords(0, w("long", 0, 45_000)),
	}}
	mustValidate(t, in, "input")
	got := Resegment(in, ResegmentOptions{})
	if len(got.Segments) != 1 || got.Segments[0].EndMs != 45_000 {
		t.Fatalf("oversized single word must pass through unchanged, got %+v", got.Segments)
	}
}

func TestResegmentWordlessSegmentPassesThrough(t *testing.T) {
	// No word timings: nothing measured to split at, so the segment survives
	// verbatim even if long.
	seg := Segment{Idx: 0, StartMs: 0, EndMs: 90_000, Text: "no words recorded"}
	in := Transcript{Language: "fa", Segments: []Segment{seg}}
	mustValidate(t, in, "input")
	got := Resegment(in, ResegmentOptions{})
	if len(got.Segments) != 1 || !reflect.DeepEqual(got.Segments[0], seg) {
		t.Fatalf("wordless segment must pass through verbatim, got %+v", got.Segments)
	}
}

func TestResegmentNilAndEmptySegments(t *testing.T) {
	got := Resegment(Transcript{Engine: "bs-asr-1", Language: "fa"}, ResegmentOptions{})
	if got.Segments != nil {
		t.Errorf("nil segments in, want nil segments out; got %+v", got.Segments)
	}
	got = Resegment(Transcript{Language: "fa", Segments: []Segment{}}, ResegmentOptions{})
	if got.Segments == nil || len(got.Segments) != 0 {
		t.Errorf("empty segments in, want empty segments out; got %#v", got.Segments)
	}
}

func TestResegmentIdxResequencedAcrossSegments(t *testing.T) {
	// Two input segments; the first splits in two. Output Idx must run 0..2.
	in := Transcript{Language: "fa", Segments: []Segment{
		segFromWords(0,
			w("a", 0, 200),
			w("b", 1000, 1200), // gap 800: boundary
		),
		segFromWords(1, w("c", 2000, 2200)),
	}}
	mustValidate(t, in, "input")
	got := Resegment(in, ResegmentOptions{})
	mustValidate(t, got, "output")
	if len(got.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(got.Segments))
	}
	for i, s := range got.Segments {
		if s.Idx != i {
			t.Errorf("segment %d Idx = %d, want %d", i, s.Idx, i)
		}
	}
}

// TestResegmentBoundariesOnlyFromWordTimes proves no timestamp is invented:
// every produced segment bound is one of the input's own word times.
func TestResegmentBoundariesOnlyFromWordTimes(t *testing.T) {
	in := Transcript{Language: "fa", Segments: []Segment{megaFixtureSegment(t, 200)}}
	mustValidate(t, in, "input")

	known := map[int]bool{}
	for _, s := range in.Segments {
		for _, wd := range s.Words {
			known[wd.StartMs] = true
			known[wd.EndMs] = true
		}
	}
	got := Resegment(in, ResegmentOptions{})
	if len(got.Segments) < 2 {
		t.Fatalf("fixture did not split (segments = %d); test needs a splitting input", len(got.Segments))
	}
	for i, s := range got.Segments {
		if !known[s.StartMs] || !known[s.EndMs] {
			t.Errorf("segment %d bounds [%d,%d] are not input word times — a timestamp was invented", i, s.StartMs, s.EndMs)
		}
	}
}

// TestResegmentDoesNotMutateInput pins purity: the input transcript's bytes are
// identical before and after the call.
func TestResegmentDoesNotMutateInput(t *testing.T) {
	in := Transcript{Language: "fa", Segments: []Segment{megaFixtureSegment(t, 120)}}
	snapshot := cloneSegments(in.Segments)
	_ = Resegment(in, ResegmentOptions{})
	if !reflect.DeepEqual(in.Segments, snapshot) {
		t.Error("Resegment mutated its input")
	}
}

// TestResegmentOutputSegmentsShareNoBacking proves two output segments never
// alias the same words array: growing one must not corrupt its neighbour.
func TestResegmentOutputSegmentsShareNoBacking(t *testing.T) {
	in := Transcript{Language: "fa", Segments: []Segment{megaFixtureSegment(t, 40)}}
	got := Resegment(in, ResegmentOptions{})
	if len(got.Segments) < 2 {
		t.Fatalf("fixture did not split; test needs >= 2 output segments")
	}
	first := got.Segments[0]
	next := cloneSegments(got.Segments[1:2])[0] // snapshot of the neighbour
	_ = append(first.Words, w("intruder", first.EndMs+10, first.EndMs+20))
	if !reflect.DeepEqual(got.Segments[1], next) {
		t.Error("appending to one output segment's words corrupted the next — shared backing array")
	}
}

// megaFixtureSegment deterministically synthesises one mega-segment of n words
// with a realistic mix of intra-phrase gaps (40-120ms), hesitations (300-600ms,
// below the pause threshold) and pauses (>= 700ms), including ZWNJ words. It is
// the reusable "provider returned one wall of words" shape.
func megaFixtureSegment(t *testing.T, n int) Segment {
	t.Helper()
	if n < 2 {
		t.Fatalf("megaFixtureSegment needs n >= 2, got %d", n)
	}
	texts := []string{"سلام", "برنامه", "می" + zwnj + "خواهم", "امروز", "درباره", "گفت" + zwnj + "وگو", "صحبت", "کنیم", "بسیار", "خب"}
	gaps := []int{60, 40, 120, 80, 900, 50, 300, 70, 1100, 90, 60, 550, 40, 750, 100}
	words := make([]Word, 0, n)
	cursor := 0
	for i := 0; i < n; i++ {
		text := texts[i%len(texts)]
		dur := 180 + (i%5)*70
		words = append(words, w(text, cursor, cursor+dur))
		cursor += dur + gaps[i%len(gaps)]
	}
	return segFromWords(0, words...)
}

// TestResegmentPropertiesSeededRandom is the property suite over deterministic
// pseudo-random transcripts (fixed seed — reproducible, no flakes): for a range
// of option sets it asserts, on every generated transcript,
//
//	(1) the flattened word sequence is preserved exactly (verbatim regroup),
//	(2) the output is Validate-green,
//	(3) every output segment is within MaxWords, and within MaxDurationMs
//	    unless it is a single word,
//	(4) no internal gap >= GapMs survives inside any output segment,
//	(5) resegmenting the output changes nothing (idempotence).
func TestResegmentPropertiesSeededRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(7)) // fixed seed: deterministic property run
	optionSets := []ResegmentOptions{
		{}, // defaults
		{GapMs: 500, MaxDurationMs: 10_000, MaxWords: 12},
		{GapMs: 1200, MaxDurationMs: 5_000, MaxWords: 7},
		{GapMs: 300, MaxDurationMs: 60_000, MaxWords: 3},
	}
	for trial := 0; trial < 60; trial++ {
		in := randomTranscript(rng)
		if err := in.Validate(); err != nil {
			t.Fatalf("trial %d: generated input invalid: %v", trial, err)
		}
		opts := optionSets[trial%len(optionSets)]
		got := Resegment(in, opts)
		eff := opts.withDefaults()

		if !reflect.DeepEqual(flatWords(got), flatWords(in)) {
			t.Fatalf("trial %d: word sequence changed", trial)
		}
		if err := got.Validate(); err != nil {
			t.Fatalf("trial %d: output invalid: %v", trial, err)
		}
		for i, s := range got.Segments {
			if len(s.Words) > eff.MaxWords {
				t.Fatalf("trial %d: segment %d has %d words, max %d", trial, i, len(s.Words), eff.MaxWords)
			}
			if len(s.Words) > 1 && spanMs(s.Words) > eff.MaxDurationMs {
				t.Fatalf("trial %d: segment %d spans %dms with %d words, max %dms", trial, i, spanMs(s.Words), len(s.Words), eff.MaxDurationMs)
			}
			for j := 0; j+1 < len(s.Words); j++ {
				if gap := s.Words[j+1].StartMs - s.Words[j].EndMs; gap >= eff.GapMs {
					t.Fatalf("trial %d: segment %d keeps an internal %dms gap >= %d", trial, i, gap, eff.GapMs)
				}
			}
		}
		again := Resegment(got, opts)
		if !reflect.DeepEqual(again, got) {
			t.Fatalf("trial %d: not idempotent", trial)
		}
	}
}

// randomTranscript generates a Validate-green transcript: 1-4 segments, each
// 1-120 words, word durations 40-800ms, gaps 0-1500ms (so pauses, hesitations
// and contiguous words all occur), ZWNJ sprinkled into word texts.
func randomTranscript(rng *rand.Rand) Transcript {
	nSegs := 1 + rng.Intn(4)
	cursor := 0
	segs := make([]Segment, 0, nSegs)
	for si := 0; si < nSegs; si++ {
		nWords := 1 + rng.Intn(120)
		words := make([]Word, 0, nWords)
		for wi := 0; wi < nWords; wi++ {
			dur := 40 + rng.Intn(760)
			text := "کلمه"
			switch rng.Intn(5) {
			case 0:
				text = "می" + zwnj + "شود"
			case 1:
				text = "word"
			}
			words = append(words, Word{Text: text, StartMs: cursor, EndMs: cursor + dur, Conf: float64(rng.Intn(100)) / 100})
			cursor += dur + rng.Intn(1500)
		}
		seg := Segment{
			Idx:     si,
			StartMs: words[0].StartMs,
			EndMs:   words[len(words)-1].EndMs + rng.Intn(200), // sometimes padded
			Text:    joinWords(words),
			Words:   words,
		}
		cursor = seg.EndMs + rng.Intn(2000)
		segs = append(segs, seg)
	}
	return Transcript{Engine: "bs-asr-1", Language: "fa", Segments: segs}
}
