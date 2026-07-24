package segmenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"blueshift/internal/asr"
	"blueshift/internal/lang"

	// Register the content languages under evaluation. The registry is the only
	// source of which languages exist; the eval never hardcodes a language code.
	_ "blueshift/internal/lang/fa"
)

// update, when set, regenerates the golden files instead of comparing against
// them. It is the sole, explicit way goldens change — never a side effect of a
// passing run. Run: go test ./eval/segment -run TestResegmentGolden -update
var update = flag.Bool("update", false, "regenerate eval goldens")

// TestResegmentGolden runs, for every registered asr-capable language, its
// committed mega-segment transcript through asr.Resegment at the DEFAULT
// thresholds and compares the produced segmentation byte-for-byte to the
// committed golden. It fails closed: a missing golden, a drift in the produced
// segmentation, or a changed fixture all fail unless -update is passed to
// deliberately regenerate.
//
// Alongside the golden it hard-asserts (never skipped, even under -update) the
// resegmentation invariants: verbatim word preservation (bytes incl. U+200C,
// timings, confidence, order), ASR-only boundary times, bound compliance,
// Validate-green output, idempotence, and that the fixture actually NEEDED
// splitting (the mega shape stays representative).
func TestResegmentGolden(t *testing.T) {
	codes := lang.Registered()
	if len(codes) == 0 {
		t.Fatal("no languages registered; eval would be a no-op")
	}

	evaluated := 0
	for _, code := range codes {
		l, err := lang.Get(code)
		if err != nil {
			t.Fatalf("Get(%q): %v", code, err)
		}
		if !declaresASR(l) {
			continue // only asr-capable languages produce transcripts to split
		}
		evaluated++

		t.Run(code, func(t *testing.T) {
			dir := filepath.Join("testdata", code)
			segs := loadSegments(t, filepath.Join(dir, "mega.json"))
			if len(segs) == 0 {
				t.Fatalf("asr-capable language %q has no mega-segment fixture at %s", code, dir)
			}
			goldenPath := filepath.Join(dir, "golden.json")

			in := asr.Transcript{Engine: "bs-asr-eval", Language: code, Segments: segs}
			if err := in.Validate(); err != nil {
				t.Fatalf("fixture transcript invalid: %v", err)
			}

			got := asr.Resegment(in, asr.ResegmentOptions{}) // zero opts = the documented defaults
			assertInvariants(t, in, got)

			want := marshalGolden(t, got.Segments)
			if *update {
				if err := os.WriteFile(goldenPath, want, 0o644); err != nil {
					t.Fatalf("write golden %s: %v", goldenPath, err)
				}
				t.Logf("updated golden %s (%d segments from %d input)", goldenPath, len(got.Segments), len(in.Segments))
				return
			}

			prev, err := os.ReadFile(goldenPath)
			if errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("golden %s missing; regenerate with: go test ./eval/segment -run TestResegmentGolden -update", goldenPath)
			}
			if err != nil {
				t.Fatalf("read golden %s: %v", goldenPath, err)
			}
			if !bytes.Equal(prev, want) {
				t.Errorf("golden drift for %q. Produced segmentation no longer matches %s.\n"+
					"If this change is intended, regenerate deliberately:\n"+
					"  go test ./eval/segment -run TestResegmentGolden -update", code, goldenPath)
			}
		})
	}
	if evaluated == 0 {
		t.Fatal("no asr-capable language evaluated; the segmentation golden would be a no-op")
	}
}

// assertInvariants hard-asserts the resegmentation contract on one run. These
// hold regardless of the golden bytes and are never bypassed by -update.
func assertInvariants(t *testing.T, in, got asr.Transcript) {
	t.Helper()

	// The mega fixture must actually need splitting, or the eval pins nothing.
	if len(got.Segments) <= len(in.Segments) {
		t.Fatalf("fixture did not resegment: %d segments in, %d out — the mega fixture must NEED splitting", len(in.Segments), len(got.Segments))
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("resegmented transcript invalid: %v", err)
	}

	// Verbatim: the flattened word sequence is unchanged — text bytes (incl.
	// U+200C ZWNJ), timings, confidence, order. Segment text is a derived view;
	// fidelity anchors here, on the words.
	if !reflect.DeepEqual(flatWords(got), flatWords(in)) {
		t.Fatal("flattened word sequence changed; resegmentation must only regroup words")
	}
	zwnjIn, zwnjOut := countZWNJ(in), countZWNJ(got)
	if zwnjIn == 0 {
		t.Fatal("fixture carries no U+200C words; the ZWNJ preservation check would be a no-op")
	}
	if zwnjOut != zwnjIn {
		t.Fatalf("ZWNJ bytes changed: %d in words before, %d after", zwnjIn, zwnjOut)
	}

	// Timestamps only from ASR: every produced bound is one of the input's own
	// word times, and each segment spans exactly first/last word (split pieces)
	// or its original bounds (untouched segments — none here, the mega splits).
	known := map[int]bool{}
	for _, s := range in.Segments {
		for _, w := range s.Words {
			known[w.StartMs] = true
			known[w.EndMs] = true
		}
		known[s.StartMs] = true
		known[s.EndMs] = true
	}
	for i, s := range got.Segments {
		if !known[s.StartMs] || !known[s.EndMs] {
			t.Fatalf("segment %d bounds [%d,%d] are not ASR-measured input times", i, s.StartMs, s.EndMs)
		}
		if s.Idx != i {
			t.Fatalf("segment %d has Idx %d; idx must be resequenced 0..n-1", i, s.Idx)
		}
		if len(s.Words) == 0 {
			t.Fatalf("segment %d is empty; resegmentation must never produce empty segments", i)
		}
		if len(s.Words) > asr.DefaultResegmentMaxWords {
			t.Fatalf("segment %d carries %d words, max %d", i, len(s.Words), asr.DefaultResegmentMaxWords)
		}
		if len(s.Words) > 1 && s.EndMs-s.StartMs > asr.DefaultResegmentMaxDurationMs {
			t.Fatalf("segment %d spans %dms, max %d", i, s.EndMs-s.StartMs, asr.DefaultResegmentMaxDurationMs)
		}
	}

	// Idempotent: resegmenting the output changes nothing.
	if again := asr.Resegment(got, asr.ResegmentOptions{}); !reflect.DeepEqual(again, got) {
		t.Fatal("Resegment is not idempotent on its own output")
	}
}

// declaresASR reports whether the language declares an asr engine slot (only
// such languages produce transcripts to resegment).
func declaresASR(l lang.Language) bool {
	for _, k := range l.EngineKeys() {
		if k == lang.EngineASR {
			return true
		}
	}
	return false
}

// flatWords flattens a transcript's words in order.
func flatWords(t asr.Transcript) []asr.Word {
	var out []asr.Word
	for _, s := range t.Segments {
		out = append(out, s.Words...)
	}
	return out
}

// countZWNJ counts U+200C occurrences across all word texts.
func countZWNJ(t asr.Transcript) int {
	n := 0
	for _, s := range t.Segments {
		for _, w := range s.Words {
			n += bytes.Count([]byte(w.Text), []byte("\u200c"))
		}
	}
	return n
}

// marshalGolden serializes the produced segments in their transcript order — a
// stable byte form (json.MarshalIndent of the ordered slice).
func marshalGolden(t *testing.T, segs []asr.Segment) []byte {
	t.Helper()
	b, err := json.MarshalIndent(segs, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	return append(b, '\n')
}

// loadSegments reads a transcript fixture (an array of asr.Segment). A missing
// file yields nil (the caller fails with a clear message). Unknown fields are
// rejected so a malformed fixture fails loudly.
func loadSegments(t *testing.T, path string) []asr.Segment {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read segments %s: %v", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var segs []asr.Segment
	if err := dec.Decode(&segs); err != nil {
		t.Fatalf("parse segments %s: %v", path, err)
	}
	return segs
}
