package asr

// resegment.go holds the deterministic pause-based resegmentation: the pure
// function that splits provider "mega-segments" (a whole take returned as one
// segment — the 2026-07-24 prod receipt: a 4-minute file as ONE 641-word
// segment) into readable, timed turns. Everything here is arithmetic over word
// timings the engine already measured:
//
//   - Timestamps only from ASR (CLAUDE.md, verbatim invariant). Every produced
//     boundary falls BETWEEN two words using their existing timings; a produced
//     segment's StartMs/EndMs are its first word's StartMs and last word's
//     EndMs. Nothing here invents, shifts, or rounds a time, and no LLM is
//     involved — the function is pure and deterministic.
//
//   - Words are verbatim. The word structs (text bytes — including U+200C ZWNJ
//     join controls — timings, confidence) cross this function unchanged, in
//     order; resegmentation only REGROUPS them. A split segment's Text is a
//     DERIVED view: its words joined with single ASCII spaces (see joinWords).
//     An untouched segment keeps its provider Text (and bounds) byte-for-byte.
//     Downstream fidelity checking anchors on the WORDS, which stay verbatim;
//     segment text is presentation, not the source of truth (see the
//     ResegmentOptions doc).
//
// Split policy, in order, per input segment:
//
//  1. Pause boundaries: every inter-word gap (next.StartMs - prev.EndMs) of at
//     least GapMs becomes a boundary. A gap that long is a deliberate pause —
//     a natural turn boundary — not articulation noise.
//  2. Bounds enforcement: any remaining group longer than MaxDurationMs or
//     wider than MaxWords is split recursively at its LARGEST internal gap
//     (ties broken toward the most word-balanced split, then leftmost), so an
//     oversized group breaks at its most pause-like point, never mid-phrase at
//     a tiny gap when a bigger one exists nearby.
//
// A segment that needs no split — no internal gap at or above GapMs and within
// both bounds — is passed through completely untouched (provider text, padded
// bounds and all). Segments without word timings can never be split (there is
// nothing measured to split at) and also pass through verbatim. Output Idx is
// resequenced 0..n-1 across the whole transcript.

import "strings"

// Default resegmentation thresholds, used by Resegment for any
// ResegmentOptions field left at or below zero. They are the single home of
// these defaults; config (SEGMENT_GAP_MS, SEGMENT_MAX_DURATION_MS,
// SEGMENT_MAX_WORDS) only overrides them.
const (
	// DefaultResegmentGapMs is the inter-word silence, in ms, treated as a
	// deliberate pause and therefore a segment boundary. 700ms sits above the
	// hesitation range of spontaneous speech — the psycholinguistics literature
	// (Goldman-Eisler 1968, "Psycholinguistics: Experiments in Spontaneous
	// Speech") puts most within-utterance hesitation pauses under ~0.5s, and
	// conversational gap studies (Heldner & Edlund 2010, "Pauses, gaps and
	// overlaps in conversations", J. Phonetics 38) cluster within-speaker
	// silences around 0.2-0.5s — while staying low enough to catch sentence
	// and turn breaks. The committed fa fixtures agree: intra-phrase word gaps
	// run 40-80ms, an order of magnitude below the threshold. Tunable per
	// deployment via SEGMENT_GAP_MS.
	DefaultResegmentGapMs = 700

	// DefaultResegmentMaxDurationMs caps one segment at 30s of audio — a turn
	// longer than that is unreadable as a single transcript row and useless as
	// a moment boundary. Tunable via SEGMENT_MAX_DURATION_MS.
	DefaultResegmentMaxDurationMs = 30_000

	// DefaultResegmentMaxWords caps one segment at 60 words — around two
	// on-screen sentences; beyond it a row reads as a wall of text. Tunable
	// via SEGMENT_MAX_WORDS.
	DefaultResegmentMaxWords = 60
)

// ResegmentOptions tunes Resegment. Any field at or below zero falls back to
// the package default, so the zero value means "the documented defaults".
type ResegmentOptions struct {
	// GapMs is the inter-word silence (ms) treated as a pause boundary: every
	// gap >= GapMs splits. Default DefaultResegmentGapMs.
	GapMs int
	// MaxDurationMs is the longest span (first word start to last word end, ms)
	// a produced segment may cover where achievable; longer groups split at
	// their largest internal gaps. A SINGLE word longer than this is kept as
	// its own segment — there is no boundary inside a word, and inventing one
	// would fabricate a timestamp. Default DefaultResegmentMaxDurationMs.
	MaxDurationMs int
	// MaxWords is the most words a produced segment may carry; wider groups
	// split at their largest internal gaps. Always achievable (down to one
	// word per segment). Default DefaultResegmentMaxWords.
	MaxWords int
}

// withDefaults resolves unset (<= 0) fields to the package defaults.
func (o ResegmentOptions) withDefaults() ResegmentOptions {
	if o.GapMs <= 0 {
		o.GapMs = DefaultResegmentGapMs
	}
	if o.MaxDurationMs <= 0 {
		o.MaxDurationMs = DefaultResegmentMaxDurationMs
	}
	if o.MaxWords <= 0 {
		o.MaxWords = DefaultResegmentMaxWords
	}
	return o
}

// Resegment splits oversized segments of t into readable timed turns per the
// policy documented at the top of this file, returning a new Transcript with
// Idx resequenced 0..n-1. It is pure and deterministic: t is never mutated,
// equal inputs produce equal outputs, and no I/O or model is involved.
//
// Guarantees, given a Validate-green input:
//
//   - The flattened word sequence (text bytes, timings, confidence, order) of
//     the output equals the input's exactly — regrouping only.
//   - Every produced boundary lies between two words; a produced segment spans
//     exactly [first word StartMs, last word EndMs]. Untouched segments keep
//     their original bounds and text byte-for-byte.
//   - Every output segment is within MaxWords, and within MaxDurationMs except
//     a single word that alone exceeds it (nothing to split there).
//   - The output is Validate-green.
//   - Idempotent: Resegment(Resegment(t, o), o) == Resegment(t, o).
func Resegment(t Transcript, opts ResegmentOptions) Transcript {
	opts = opts.withDefaults()
	out := Transcript{Engine: t.Engine, Language: t.Language, Raw: t.Raw}
	if t.Segments == nil {
		return out
	}
	out.Segments = make([]Segment, 0, len(t.Segments))
	for _, seg := range t.Segments {
		out.Segments = append(out.Segments, splitSegment(seg, opts)...)
	}
	for i := range out.Segments {
		out.Segments[i].Idx = i
	}
	return out
}

// splitSegment turns one input segment into its ordered output segments. A
// segment that ends up in a single group is returned exactly as given —
// provider text, padded bounds, shared Words backing — so an already-readable
// segment survives byte-for-byte. Split groups become fresh segments (bounds
// from their first/last word, text derived by joinWords, copied word slices so
// no two output segments share backing storage).
func splitSegment(seg Segment, opts ResegmentOptions) []Segment {
	if len(seg.Words) < 2 {
		// Zero or one word: no inter-word boundary exists. Never invent one.
		return []Segment{seg}
	}
	groups := make([][]Word, 0, 1)
	for _, g := range gapSplit(seg.Words, opts.GapMs) {
		groups = append(groups, enforceBounds(g, opts)...)
	}
	if len(groups) == 1 {
		return []Segment{seg}
	}
	out := make([]Segment, 0, len(groups))
	for _, g := range groups {
		out = append(out, segmentFromWords(g))
	}
	return out
}

// gapSplit cuts words at every inter-word gap of at least gapMs, returning the
// ordered groups. Each returned group therefore has all internal gaps below
// gapMs. Groups are subslices of words; enforceBounds/segmentFromWords copy
// before anything escapes.
func gapSplit(words []Word, gapMs int) [][]Word {
	groups := make([][]Word, 0, 1)
	start := 0
	for i := 0; i+1 < len(words); i++ {
		if words[i+1].StartMs-words[i].EndMs >= gapMs {
			groups = append(groups, words[start:i+1])
			start = i + 1
		}
	}
	return append(groups, words[start:])
}

// enforceBounds recursively splits a group that exceeds MaxDurationMs or
// MaxWords at its widest internal gap until every piece is within bounds or
// down to a single word (a lone word longer than MaxDurationMs cannot split —
// there is no boundary inside a word). Both sides of every split are non-empty,
// so the recursion strictly shrinks and terminates.
func enforceBounds(words []Word, opts ResegmentOptions) [][]Word {
	if len(words) < 2 || (spanMs(words) <= opts.MaxDurationMs && len(words) <= opts.MaxWords) {
		return [][]Word{words}
	}
	k := widestGapIndex(words)
	return append(enforceBounds(words[:k+1], opts), enforceBounds(words[k+1:], opts)...)
}

// widestGapIndex returns the index k of the widest inter-word gap (between
// words[k] and words[k+1]) — the most pause-like point to break at. Ties pick
// the most word-balanced split (left piece size closest to half), and a
// remaining tie picks the leftmost, keeping the choice fully deterministic and
// avoiding degenerate one-word shavings when all gaps are equal.
func widestGapIndex(words []Word) int {
	best, bestGap, bestDist := 0, -1, len(words)
	half := len(words) / 2
	for k := 0; k+1 < len(words); k++ {
		gap := words[k+1].StartMs - words[k].EndMs
		dist := k + 1 - half
		if dist < 0 {
			dist = -dist
		}
		if gap > bestGap || (gap == bestGap && dist < bestDist) {
			best, bestGap, bestDist = k, gap, dist
		}
	}
	return best
}

// spanMs is the audible span a group covers: first word start to last word end.
func spanMs(words []Word) int {
	return words[len(words)-1].EndMs - words[0].StartMs
}

// segmentFromWords builds a fresh segment over a copy of words. Bounds come
// only from the words' own measured times (start = first word's StartMs, end =
// last word's EndMs); Text is the derived joined view. Idx is assigned by the
// caller's global resequencing.
func segmentFromWords(words []Word) Segment {
	w := make([]Word, len(words))
	copy(w, words)
	return Segment{
		StartMs: w[0].StartMs,
		EndMs:   w[len(w)-1].EndMs,
		Text:    joinWords(w),
		Words:   w,
	}
}

// joinWords derives a split segment's Text: the verbatim word texts joined
// with single ASCII spaces (U+0020). This is the documented join rule: word
// INTERIOR bytes — including U+200C ZWNJ join controls — are untouched because
// the word strings themselves are copied verbatim; only the separator between
// words is chosen here. Caption fidelity checking anchors on the words, so the
// derived text is a readable view, never the measured source of truth.
func joinWords(words []Word) string {
	var b strings.Builder
	for i, w := range words {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(w.Text)
	}
	return b.String()
}
