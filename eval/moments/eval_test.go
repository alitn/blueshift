package momentseval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"blueshift/internal/lang"
	"blueshift/internal/llm"
	"blueshift/internal/moments"
	"blueshift/internal/pipeline"

	// Register the content languages under evaluation. The registry is the only
	// source of which languages exist; the eval never hardcodes a language code.
	_ "blueshift/internal/lang/fa"
)

// update, when set, regenerates the golden files instead of comparing against
// them. It is the sole, explicit way goldens change — never a side effect of a
// passing run. Run: go test ./eval/moments -run TestMomentSelectionGolden -update
var update = flag.Bool("update", false, "regenerate eval goldens")

// evalEngineLabel is the neutral engine label the golden runs under. The fake
// engine registers under it and the language declares an llm slot bound to it.
const evalEngineLabel = "bs-lm-1"

// TestMomentSelectionGolden runs, for every registered llm-capable language,
// its committed speaker-aware transcript through the moment selector with a
// fake-backed llm.Client replaying the committed model response, derives the
// rows the stage would persist (pipeline.DeriveMomentRows — including the
// QUOTE-derived word-accurate start_ms/end_ms), and compares them to the
// committed golden, pinning the derived ms values byte-stable. It fails
// closed: a missing golden, a drift in the produced proposals or derived
// times, or changed fixtures all fail unless -update is passed to
// deliberately regenerate.
func TestMomentSelectionGolden(t *testing.T) {
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
		if !declaresLLM(l) {
			continue // only llm-capable languages get moment selection
		}
		evaluated++

		t.Run(code, func(t *testing.T) {
			dir := filepath.Join("testdata", code)
			segs := loadSegments(t, filepath.Join(dir, "segments.json"))
			if len(segs) == 0 {
				t.Fatalf("llm-capable language %q has no moment fixtures at %s", code, dir)
			}
			response := loadFile(t, filepath.Join(dir, "response.json"))
			goldenPath := filepath.Join(dir, "golden.json")

			// A fake-backed Client replays the recorded response through the real
			// schema-validate/one-retry/audit loop — the same path a provider-backed
			// engine takes — so the proposals are produced by the actual selector
			// code, including the verbatim-quote and span validation.
			fe := llm.NewFakeEngine(evalEngineLabel, "bs-lm-eval", response)
			client, err := llm.NewFakeClient(nil, fe)
			if err != nil {
				t.Fatalf("NewFakeClient: %v", err)
			}
			eng := moments.Engine{Gen: client, Labels: moments.LangLabelResolver{Label: evalEngineLabel}}

			props, err := eng.SelectMoments(context.Background(), code, 1, 1, segs)
			if err != nil {
				t.Fatalf("SelectMoments(%q): %v", code, err)
			}
			// Derive the persisted rows exactly as the stage does: the quote is
			// located in the span's word data and start_ms/end_ms are its first/
			// last word's measured times — the golden pins those values.
			rows, err := pipeline.DeriveMomentRows(props, segs)
			if err != nil {
				t.Fatalf("DeriveMomentRows(%q): %v", code, err)
			}
			want := marshalGolden(t, rows)

			if *update {
				if err := os.WriteFile(goldenPath, want, 0o644); err != nil {
					t.Fatalf("write golden %s: %v", goldenPath, err)
				}
				t.Logf("updated golden %s (%d moments)", goldenPath, len(rows))
				return
			}

			got, err := os.ReadFile(goldenPath)
			if errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("golden %s missing; regenerate with: go test ./eval/moments -run TestMomentSelectionGolden -update", goldenPath)
			}
			if err != nil {
				t.Fatalf("read golden %s: %v", goldenPath, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("golden drift for %q. Produced proposals no longer match %s.\n"+
					"If this change is intended, regenerate deliberately:\n"+
					"  go test ./eval/moments -run TestMomentSelectionGolden -update", code, goldenPath)
			}
		})
	}
	if evaluated == 0 {
		t.Fatal("no llm-capable language evaluated; the moment-selection golden would be a no-op")
	}
}

// declaresLLM reports whether the language declares an llm engine slot (only
// such languages get moment selection).
func declaresLLM(l lang.Language) bool {
	for _, k := range l.EngineKeys() {
		if k == lang.EngineLLM {
			return true
		}
	}
	return false
}

// marshalGolden serializes the derived rows — already rank-ordered by the
// engine, each carrying its quote-derived word-accurate times — to the stable
// golden byte form.
func marshalGolden(t *testing.T, rows []pipeline.MomentRow) []byte {
	t.Helper()
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	return append(b, '\n')
}

// loadSegments reads a speaker-aware transcript fixture (an array of
// pipeline.MomentSegment: the asr.Segment fields plus speaker_key). A missing
// file yields nil (the caller fails with a clear message). Unknown fields are
// rejected so a malformed fixture fails loudly.
func loadSegments(t *testing.T, path string) []pipeline.MomentSegment {
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
	var segs []pipeline.MomentSegment
	if err := dec.Decode(&segs); err != nil {
		t.Fatalf("parse segments %s: %v", path, err)
	}
	return segs
}

// loadFile reads a raw fixture file (the recorded model response).
func loadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return b
}
