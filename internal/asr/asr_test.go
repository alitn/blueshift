package asr

import (
	"context"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forbidden is the vendor-leak name list (mirrors the Makefile gate and the
// /internal/llm test list). This package's non-test files may legitimately name a
// provider in a future concrete engine (the gate does not scan /internal/asr), so
// these assertions target only what a caller can SEE: sentinel error strings, the
// neutral engine label, and everything a returned Transcript carries as data plus
// the committed fixtures that feed `make demo`.
var forbidden = []string{
	"chirp", "gemini", "vertex", "google", "speech-to-text",
	"anthropic", "claude", "elevenlabs", "openai", "whisper",
	"deepgram", "assemblyai", "generativelanguage", "aiplatform",
}

func assertNoLeak(t *testing.T, what, s string) {
	t.Helper()
	low := strings.ToLower(s)
	for _, name := range forbidden {
		if strings.Contains(low, name) {
			t.Errorf("%s leaks provider name %q: %q", what, name, s)
		}
	}
}

// --- Validate: valid transcripts pass ---------------------------------------

func TestValidateAcceptsWellFormed(t *testing.T) {
	// Many random-but-valid transcripts must all validate. This is the positive
	// half of the property: the generator only ever produces monotonic,
	// non-overlapping, in-bounds structures.
	for seed := uint64(1); seed <= 500; seed++ {
		tr := validTranscript(mrand.New(mrand.NewPCG(seed, seed*2+1)))
		if err := tr.Validate(); err != nil {
			t.Fatalf("seed %d: well-formed transcript rejected: %v\n%s", seed, err, dump(tr))
		}
	}
}

func TestValidateAcceptsEmpty(t *testing.T) {
	// An empty transcript is structurally valid (no words can violate anything);
	// "did we get anything back" is a separate, caller-level concern.
	if err := (Transcript{}).Validate(); err != nil {
		t.Fatalf("empty transcript rejected: %v", err)
	}
	if err := (Transcript{Segments: []Segment{{Idx: 0, StartMs: 0, EndMs: 100, Text: "x"}}}).Validate(); err != nil {
		t.Fatalf("segment with no words rejected: %v", err)
	}
}

// --- Validate: each malformed class is rejected (property tests) -------------

// mutation names a malformed class, the mutator that injects exactly that
// violation into a valid transcript, and a substring the resulting error must
// carry so we know the RIGHT invariant tripped (not some incidental one).
type mutation struct {
	name   string
	want   string // substring the rejection message must contain
	mutate func(*mrand.Rand, *Transcript) bool
}

var mutations = []mutation{
	{
		name: "word overlap",
		want: "overlaps previous",
		mutate: func(r *mrand.Rand, tr *Transcript) bool {
			s, w := pickWord(r, tr, 1) // a word with a predecessor
			if s < 0 {
				return false
			}
			prev := tr.Segments[s].Words[w-1]
			cur := &tr.Segments[s].Words[w]
			// Start inside the previous word's span: after its start, before its end.
			cur.StartMs = prev.EndMs - 1
			if cur.EndMs < cur.StartMs {
				cur.EndMs = cur.StartMs + 1
			}
			return cur.StartMs > prev.StartMs && cur.StartMs < prev.EndMs && cur.EndMs <= tr.Segments[s].EndMs
		},
	},
	{
		name: "word non-monotonic start",
		want: "before previous word start",
		mutate: func(r *mrand.Rand, tr *Transcript) bool {
			s, w := pickWord(r, tr, 1)
			if s < 0 {
				return false
			}
			prev := tr.Segments[s].Words[w-1]
			cur := &tr.Segments[s].Words[w]
			// Start strictly before the previous word's START (backwards in time),
			// while staying inside the segment so the bounds check does not fire first.
			cur.StartMs = prev.StartMs - 1
			cur.EndMs = cur.StartMs + 1
			return cur.StartMs >= tr.Segments[s].StartMs && cur.StartMs < prev.StartMs
		},
	},
	{
		name: "word end before start",
		want: "is non-monotonic: end",
		mutate: func(r *mrand.Rand, tr *Transcript) bool {
			s, w := pickWord(r, tr, 0)
			if s < 0 {
				return false
			}
			cur := &tr.Segments[s].Words[w]
			cur.EndMs = cur.StartMs - 1
			return cur.EndMs >= 0 && cur.EndMs < cur.StartMs
		},
	},
	{
		name: "word out of bounds (past segment end)",
		want: "outside segment bounds",
		mutate: func(r *mrand.Rand, tr *Transcript) bool {
			s := pickSegment(r, tr, 1)
			if s < 0 {
				return false
			}
			last := &tr.Segments[s].Words[len(tr.Segments[s].Words)-1]
			last.EndMs = tr.Segments[s].EndMs + 5
			return true
		},
	},
	{
		name: "word out of bounds (before segment start)",
		want: "outside segment bounds",
		mutate: func(r *mrand.Rand, tr *Transcript) bool {
			s := pickSegment(r, tr, 1)
			if s < 0 || tr.Segments[s].StartMs == 0 {
				return false
			}
			first := &tr.Segments[s].Words[0]
			first.StartMs = tr.Segments[s].StartMs - 1
			return true
		},
	},
	{
		name: "segment overlap",
		want: "segment 1 overlaps previous", // uses a >=2 segment transcript
		mutate: func(r *mrand.Rand, tr *Transcript) bool {
			if len(tr.Segments) < 2 {
				return false
			}
			// Pull segment 1's start back inside segment 0. Its words start at the old
			// (later) start, so they remain in bounds; only the segment overlap fires.
			tr.Segments[1].StartMs = tr.Segments[0].EndMs - 1
			return tr.Segments[1].StartMs < tr.Segments[0].EndMs
		},
	},
	{
		name: "segment non-increasing idx",
		want: "non-increasing idx",
		mutate: func(r *mrand.Rand, tr *Transcript) bool {
			if len(tr.Segments) < 2 {
				return false
			}
			tr.Segments[1].Idx = tr.Segments[0].Idx
			return true
		},
	},
	{
		name: "segment end before start",
		want: "segment 0 is non-monotonic",
		mutate: func(r *mrand.Rand, tr *Transcript) bool {
			tr.Segments[0].EndMs = tr.Segments[0].StartMs - 1
			return tr.Segments[0].EndMs >= 0
		},
	},
	{
		name: "segment negative timing",
		want: "negative timing",
		mutate: func(r *mrand.Rand, tr *Transcript) bool {
			tr.Segments[0].StartMs = -1
			return true
		},
	},
}

func TestValidateRejectsEachMalformedClass(t *testing.T) {
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			applied := 0
			for seed := uint64(1); seed <= 400 && applied < 60; seed++ {
				r := mrand.New(mrand.NewPCG(seed, seed*7+3))
				tr := validMultiTranscript(r)
				// Sanity: the base transcript is valid before we corrupt it.
				if err := tr.Validate(); err != nil {
					t.Fatalf("seed %d: base transcript invalid before mutation: %v", seed, err)
				}
				corrupt := deepCopy(tr)
				if !m.mutate(r, &corrupt) {
					continue // this seed's shape didn't fit the mutation; try another
				}
				applied++
				err := corrupt.Validate()
				if err == nil {
					t.Fatalf("seed %d: %s not rejected\n%s", seed, m.name, dump(corrupt))
				}
				if !errors.Is(err, ErrInvalidTranscript) {
					t.Fatalf("seed %d: %s error not ErrInvalidTranscript: %v", seed, m.name, err)
				}
				if !strings.Contains(err.Error(), m.want) {
					t.Fatalf("seed %d: %s error %q missing %q", seed, m.name, err.Error(), m.want)
				}
				assertNoLeak(t, m.name+" rejection", err.Error())
			}
			if applied == 0 {
				t.Fatalf("%s: no seed produced a fitting shape (test would prove nothing)", m.name)
			}
		})
	}
}

// --- Registry ---------------------------------------------------------------

// stubEngine is a minimal Engine for registry tests (no fixtures needed).
type stubEngine struct{ lbl string }

func (s stubEngine) Label() string { return s.lbl }
func (s stubEngine) Transcribe(context.Context, TranscribeRequest) (Transcript, error) {
	return Transcript{Engine: s.lbl}, nil
}

func TestRegistryGet(t *testing.T) {
	reg, err := NewRegistry(stubEngine{"bs-asr-1"}, stubEngine{"bs-asr-2"})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	e, err := reg.Get("bs-asr-1")
	if err != nil {
		t.Fatalf("Get(bs-asr-1): %v", err)
	}
	if e.Label() != "bs-asr-1" {
		t.Errorf("Label() = %q, want bs-asr-1", e.Label())
	}
	if got, want := reg.Labels(), []string{"bs-asr-1", "bs-asr-2"}; !equalStrings(got, want) {
		t.Errorf("Labels() = %v, want %v", got, want)
	}
}

func TestRegistryUnknownLabelIsExplicitError(t *testing.T) {
	reg, err := NewRegistry(stubEngine{"bs-asr-1"})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for _, label := range []string{"bs-asr-9", "", "BS-ASR-1", "asr"} {
		_, err := reg.Get(label)
		if !errors.Is(err, ErrUnknownEngine) {
			t.Errorf("Get(%q) err = %v, want ErrUnknownEngine", label, err)
		}
		assertNoLeak(t, "unknown-engine error", errString(err))
	}
}

func TestNewRegistryRejectsMisconfig(t *testing.T) {
	cases := []struct {
		name    string
		engines []Engine
	}{
		{"empty set", nil},
		{"empty label", []Engine{stubEngine{""}}},
		{"duplicate label", []Engine{stubEngine{"bs-asr-1"}, stubEngine{"bs-asr-1"}}},
		{"nil engine", []Engine{nil}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewRegistry(c.engines...); err == nil {
				t.Fatalf("NewRegistry(%s) = nil err, want rejection", c.name)
			}
		})
	}
}

// --- Vendor neutrality ------------------------------------------------------

func TestSentinelErrorsAreNeutral(t *testing.T) {
	for _, err := range []error{ErrUnknownEngine, ErrInvalidTranscript, errNoFixture} {
		assertNoLeak(t, "sentinel error", err.Error())
	}
}

func TestFixturesAreVendorNeutral(t *testing.T) {
	// The committed fixtures ship into `make demo` and feed client-visible caption
	// text, so nothing in them may name a provider.
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	seen := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		seen++
		b, err := os.ReadFile(filepath.Join("testdata", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		assertNoLeak(t, "fixture "+e.Name(), string(b))
	}
	if seen == 0 {
		t.Fatal("no fixtures found under testdata")
	}
}

// --- helpers ----------------------------------------------------------------

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// validTranscript builds a random, well-formed Transcript (1..4 segments, each
// 0..5 words) whose every timing invariant holds by construction.
func validTranscript(r *mrand.Rand) Transcript {
	return buildTranscript(r, 1+r.IntN(4), 0)
}

// validMultiTranscript builds a random, well-formed Transcript guaranteed to have
// >=2 segments and >=2 words per segment, so every mutator has a shape to corrupt.
func validMultiTranscript(r *mrand.Rand) Transcript {
	return buildTranscript(r, 2+r.IntN(3), 2)
}

// buildTranscript lays segments end-to-end with gaps, filling each with monotonic,
// non-overlapping words strictly inside the segment. minWords forces at least that
// many words per segment (0 allows empty segments).
func buildTranscript(r *mrand.Rand, nSeg, minWords int) Transcript {
	segs := make([]Segment, nSeg)
	cursor := r.IntN(50)
	for i := range segs {
		segStart := cursor + r.IntN(40)
		nWords := minWords + r.IntN(4)
		words := make([]Word, nWords)
		// First word starts strictly after the segment start so a "-1" mutation on
		// the first word's start still lands inside the segment (bounds check clean).
		wc := segStart + 1 + r.IntN(20)
		for j := range words {
			wStart := wc + r.IntN(25)
			wEnd := wStart + 20 + r.IntN(400)
			words[j] = Word{
				Text:    fmt.Sprintf("w%d_%d", i, j),
				StartMs: wStart,
				EndMs:   wEnd,
				Conf:    0.90 + float64(r.IntN(10))/100,
			}
			wc = wEnd
		}
		// Segment ends at/after the last word, plus a tail, so words stay in bounds.
		segEnd := wc + 1 + r.IntN(60)
		segs[i] = Segment{Idx: i, StartMs: segStart, EndMs: segEnd, Text: "seg", Words: words}
		cursor = segEnd + 1 + r.IntN(120) // strictly after -> non-overlapping segments
	}
	return Transcript{Engine: "bs-asr-1", Language: "fa", Segments: segs}
}

// deepCopy clones a transcript so a mutator cannot disturb the pristine original.
func deepCopy(tr Transcript) Transcript {
	tr.Segments = cloneSegments(tr.Segments)
	return tr
}

// pickWord returns a (segment, word) index where word >= minWordIdx, or (-1,-1)
// if no segment has enough words.
func pickWord(r *mrand.Rand, tr *Transcript, minWordIdx int) (int, int) {
	for _, s := range r.Perm(len(tr.Segments)) {
		if len(tr.Segments[s].Words) > minWordIdx {
			w := minWordIdx + r.IntN(len(tr.Segments[s].Words)-minWordIdx)
			return s, w
		}
	}
	return -1, -1
}

// pickSegment returns a segment index with at least minWords words, or -1.
func pickSegment(r *mrand.Rand, tr *Transcript, minWords int) int {
	for _, s := range r.Perm(len(tr.Segments)) {
		if len(tr.Segments[s].Words) >= minWords {
			return s
		}
	}
	return -1
}

func dump(tr Transcript) string {
	var b strings.Builder
	for _, s := range tr.Segments {
		fmt.Fprintf(&b, "seg idx=%d [%d,%d]\n", s.Idx, s.StartMs, s.EndMs)
		for _, w := range s.Words {
			fmt.Fprintf(&b, "  word %q [%d,%d]\n", w.Text, w.StartMs, w.EndMs)
		}
	}
	return b.String()
}
