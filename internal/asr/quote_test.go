package asr

import (
	"strings"
	"testing"
)

// quoteSegs is a two-segment span whose texts are the joinWords view of their
// words — the shape resegmentation produces. Word times deliberately sit
// strictly inside the (padded) segment bounds so word-accuracy is observable.
func quoteSegs() []Segment {
	return []Segment{
		{Idx: 0, StartMs: 0, EndMs: 3000, Text: "سلام به برنامه خوش آمدید", Words: []Word{
			{Text: "سلام", StartMs: 100, EndMs: 520, Conf: 0.98},
			{Text: "به", StartMs: 600, EndMs: 720, Conf: 0.95},
			{Text: "برنامه", StartMs: 760, EndMs: 1280, Conf: 0.97},
			{Text: "خوش", StartMs: 1340, EndMs: 1600, Conf: 0.96},
			{Text: "آمدید", StartMs: 1660, EndMs: 2200, Conf: 0.94},
		}},
		{Idx: 1, StartMs: 3000, EndMs: 5000, Text: "خیلی خوش" + zwnj + "حالم که اینجا هستم", Words: []Word{
			{Text: "خیلی", StartMs: 3100, EndMs: 3300, Conf: 0.96},
			{Text: "خوش" + zwnj + "حالم", StartMs: 3360, EndMs: 3940, Conf: 0.95},
			{Text: "که", StartMs: 4000, EndMs: 4120, Conf: 0.94},
			{Text: "اینجا", StartMs: 4180, EndMs: 4480, Conf: 0.96},
			{Text: "هستم", StartMs: 4540, EndMs: 4800, Conf: 0.95},
		}},
	}
}

// TestLocateQuoteWordAccurate: a quote in the middle of a segment maps to ITS
// words' times, strictly tighter than the segment bounds.
func TestLocateQuoteWordAccurate(t *testing.T) {
	start, end, err := LocateQuote(quoteSegs()[:1], "به برنامه")
	if err != nil {
		t.Fatalf("LocateQuote: %v", err)
	}
	if start != 600 || end != 1280 {
		t.Errorf("times = %d..%d, want 600..1280 (the quote's own words)", start, end)
	}
}

// TestLocateQuoteCrossesSegmentBoundary: a quote spanning the end of one
// segment and the start of the next aligns across the single-space join.
func TestLocateQuoteCrossesSegmentBoundary(t *testing.T) {
	start, end, err := LocateQuote(quoteSegs(), "آمدید خیلی")
	if err != nil {
		t.Fatalf("LocateQuote: %v", err)
	}
	if start != 1660 || end != 3300 {
		t.Errorf("times = %d..%d, want 1660..3300 (last word of seg 0 to first of seg 1)", start, end)
	}
}

// TestLocateQuoteFirstOccurrence: an ambiguous quote occurring twice in the
// span deterministically takes the FIRST occurrence's times.
func TestLocateQuoteFirstOccurrence(t *testing.T) {
	segs := []Segment{
		{Idx: 0, Text: "سلام دوباره سلام", Words: []Word{
			{Text: "سلام", StartMs: 0, EndMs: 400},
			{Text: "دوباره", StartMs: 500, EndMs: 900},
			{Text: "سلام", StartMs: 1000, EndMs: 1400},
		}},
	}
	start, end, err := LocateQuote(segs, "سلام")
	if err != nil {
		t.Fatalf("LocateQuote: %v", err)
	}
	if start != 0 || end != 400 {
		t.Errorf("times = %d..%d, want 0..400 (the first occurrence)", start, end)
	}
}

// TestLocateQuoteZWNJIntact: the ZWNJ-carrying word aligns as one word — the
// join rule never splits inside a word.
func TestLocateQuoteZWNJIntact(t *testing.T) {
	start, end, err := LocateQuote(quoteSegs(), "خوش"+zwnj+"حالم")
	if err != nil {
		t.Fatalf("LocateQuote: %v", err)
	}
	if start != 3360 || end != 3940 {
		t.Errorf("times = %d..%d, want 3360..3940 (the single ZWNJ word)", start, end)
	}
	// The space-joined lookalike (ZWNJ replaced by a space) is a DIFFERENT byte
	// sequence and must not align to it.
	if _, _, err := LocateQuote(quoteSegs(), strings.ReplaceAll("خوش"+zwnj+"حالم", zwnj, " ")); err == nil {
		t.Error("space-for-ZWNJ lookalike aligned, want failure (verbatim bytes only)")
	}
}

// TestLocateQuoteFailures: no words, a quote absent from the word data, and a
// blank quote all fail explicitly — the caller's invalid-output path, never a
// guessed time.
func TestLocateQuoteFailures(t *testing.T) {
	if _, _, err := LocateQuote([]Segment{{Text: "متن بدون کلمه"}}, "متن"); err == nil {
		t.Error("no-words span aligned, want error")
	}
	if _, _, err := LocateQuote(quoteSegs(), "این جمله وجود ندارد"); err == nil {
		t.Error("absent quote aligned, want error")
	}
	if _, _, err := LocateQuote(quoteSegs(), "  "); err == nil {
		t.Error("blank quote aligned, want error")
	}
}
