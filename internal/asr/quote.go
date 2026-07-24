package asr

// quote.go holds the word-accurate quote locator: the pure function that maps
// a verbatim quote back onto the measured word timings of the segments it was
// copied from. The moments stage uses it to derive a moment's start_ms/end_ms
// from its quote's FIRST and LAST word — so moment precision is independent of
// segment length, and every persisted time is an ASR-measured word time
// (CLAUDE.md, verbatim invariant: models quote text, they never measure; the
// caller looks the times up here).
//
// The alignment is deterministic and uses the SAME join rule as
// resegmentation (joinWords: verbatim word texts joined with single ASCII
// spaces): the span's words are joined, the quote's first occurrence is found
// by byte offset, and the offset range maps back to word indexes through the
// join offsets. A quote that validated as a substring of text derived by the
// same rule therefore always aligns; a failure means the quote and the word
// data disagree, which the caller must treat as invalid output, never guess
// around.

import (
	"errors"
	"fmt"
	"strings"
)

// LocateQuote finds the FIRST occurrence of quote within the words of segs
// (taken in the order given — the caller passes the moment's segment span in
// idx order) and returns the quote's first word's StartMs and last word's
// EndMs. The words are joined across all segments with the joinWords rule
// (single ASCII spaces), so offsets are consistent with resegmentation-derived
// segment text. It fails when the span carries no word data or when the quote
// does not occur in the words-derived join — the caller's invalid-output path.
func LocateQuote(segs []Segment, quote string) (startMs, endMs int, err error) {
	if strings.TrimSpace(quote) == "" {
		return 0, 0, errors.New("asr: locate quote: empty quote")
	}
	var words []Word
	for _, s := range segs {
		words = append(words, s.Words...)
	}
	if len(words) == 0 {
		return 0, 0, errors.New("asr: locate quote: span has no word timings")
	}
	joined := joinWords(words)
	pos := strings.Index(joined, quote)
	if pos < 0 {
		return 0, 0, fmt.Errorf("asr: locate quote: quote not found in the span's word data")
	}
	lo, hi := pos, pos+len(quote) // byte range of the first occurrence

	// Walk the join offsets: word i occupies [off, off+len(text)); one separator
	// byte follows each word but the last. The quote's first/last words are the
	// first/last whose byte ranges overlap [lo, hi) — separator-only edges (a
	// quote with a leading/trailing space) never select a word by themselves.
	first, last := -1, -1
	off := 0
	for i, w := range words {
		wLo, wHi := off, off+len(w.Text)
		if wLo < hi && wHi > lo {
			if first < 0 {
				first = i
			}
			last = i
		}
		off = wHi + 1 // the single-space separator
	}
	if first < 0 {
		// Unreachable for a non-blank quote found in the join, kept as a guard.
		return 0, 0, errors.New("asr: locate quote: quote overlaps no word")
	}
	return words[first].StartMs, words[last].EndMs, nil
}
