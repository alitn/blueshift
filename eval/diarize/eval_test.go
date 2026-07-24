package diarizeeval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"blueshift/internal/asr"
	"blueshift/internal/diarize"
	"blueshift/internal/lang"
	"blueshift/internal/llm"

	// Register the content languages under evaluation. The registry is the only
	// source of which languages exist; the eval never hardcodes a language code.
	_ "blueshift/internal/lang/fa"
)

// update, when set, regenerates the golden files instead of comparing against
// them. It is the sole, explicit way goldens change — never a side effect of a
// passing run. Run: go test ./eval/diarize -run TestDiarizeAnchorMergeGolden -update
var update = flag.Bool("update", false, "regenerate eval goldens")

// evalEngineLabel is the neutral engine label the golden runs under. The fake
// engine registers under it and the language declares an llm slot bound to it.
const evalEngineLabel = "bs-lm-1"

// goldenAssignment is one entry in a golden: a segment idx and its produced
// episode-local speaker label. The golden is the grouping serialized in idx order
// (never a Go map, whose iteration/marshal order would not be stable).
type goldenAssignment struct {
	Idx        int    `json:"idx"`
	SpeakerKey string `json:"speaker_key"`
}

// TestDiarizeAnchorMergeGolden runs, for every registered llm-capable language,
// its committed transcript through the diarizer with a fake-backed llm.Client
// replaying the committed model response, and compares the produced grouping to
// the committed golden. It fails closed: a missing golden, a drift in the produced
// grouping, or changed fixtures all fail unless -update is passed to deliberately
// regenerate.
func TestDiarizeAnchorMergeGolden(t *testing.T) {
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
			continue // only llm-capable languages are diarizable
		}
		evaluated++

		t.Run(code, func(t *testing.T) {
			dir := filepath.Join("testdata", code)
			segs := loadSegments(t, filepath.Join(dir, "segments.json"))
			if len(segs) == 0 {
				t.Fatalf("llm-capable language %q has no diarization fixtures at %s", code, dir)
			}
			response := loadFile(t, filepath.Join(dir, "response.json"))
			goldenPath := filepath.Join(dir, "golden.json")

			// A fake-backed Client replays the recorded response through the real
			// schema-validate/one-retry/audit loop — the same path a provider-backed
			// engine takes — so the grouping is produced by the actual diarizer code.
			fe := llm.NewFakeEngine(evalEngineLabel, "bs-lm-eval", response)
			client, err := llm.NewFakeClient(nil, fe)
			if err != nil {
				t.Fatalf("NewFakeClient: %v", err)
			}
			eng := diarize.Engine{Gen: client, Labels: diarize.LangLabelResolver{Label: evalEngineLabel}}

			byIdx, err := eng.Diarize(context.Background(), code, 1, 1, segs)
			if err != nil {
				t.Fatalf("Diarize(%q): %v", code, err)
			}
			want := marshalGolden(t, byIdx)

			if *update {
				if err := os.WriteFile(goldenPath, want, 0o644); err != nil {
					t.Fatalf("write golden %s: %v", goldenPath, err)
				}
				t.Logf("updated golden %s (%d segments)", goldenPath, len(byIdx))
				return
			}

			got, err := os.ReadFile(goldenPath)
			if errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("golden %s missing; regenerate with: go test ./eval/diarize -run TestDiarizeAnchorMergeGolden -update", goldenPath)
			}
			if err != nil {
				t.Fatalf("read golden %s: %v", goldenPath, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("golden drift for %q. Produced grouping no longer matches %s.\n"+
					"If this change is intended, regenerate deliberately:\n"+
					"  go test ./eval/diarize -run TestDiarizeAnchorMergeGolden -update", code, goldenPath)
			}
		})
	}
	if evaluated == 0 {
		t.Fatal("no llm-capable language evaluated; the diarization golden would be a no-op")
	}
}

// TestDiarizeScaleGolden is the full-episode-scale golden: a synthetic
// 249-segment fa transcript mirroring the real production episode that broke
// the flat per-segment contract (same segment count, ZWNJ-bearing Persian text,
// realistic inter-segment gaps; 2026-07-24 receipt). The committed turn-range
// response (50 alternating host/guest turns tiling 0..248 exactly) is replayed
// through the real validate/retry loop and the produced per-segment grouping is
// byte-compared to the committed golden — proving the range validator and the
// range -> per-segment conversion at the scale that previously hard-failed.
// The fixture omits words arrays deliberately: diarize never reads them (the
// request is idx+text only, proven by the internal/diarize unit tests), and the
// dimensions that broke the old contract are segment count and text volume.
func TestDiarizeScaleGolden(t *testing.T) {
	dir := filepath.Join("testdata", "fa")
	segs := loadSegments(t, filepath.Join(dir, "scale_segments.json"))
	if len(segs) == 0 {
		t.Fatalf("scale fixture missing at %s", dir)
	}
	if len(segs) != 249 {
		t.Fatalf("scale fixture has %d segments, want 249 (the prod episode shape)", len(segs))
	}
	response := loadFile(t, filepath.Join(dir, "scale_response.json"))
	goldenPath := filepath.Join(dir, "scale_golden.json")

	fe := llm.NewFakeEngine(evalEngineLabel, "bs-lm-eval", response)
	client, err := llm.NewFakeClient(nil, fe)
	if err != nil {
		t.Fatalf("NewFakeClient: %v", err)
	}
	eng := diarize.Engine{Gen: client, Labels: diarize.LangLabelResolver{Label: evalEngineLabel}}

	byIdx, err := eng.Diarize(context.Background(), "fa", 1, 1, segs)
	if err != nil {
		t.Fatalf("Diarize(fa, 249 segments): %v", err)
	}
	if len(byIdx) != len(segs) {
		t.Fatalf("grouping covers %d of %d segments", len(byIdx), len(segs))
	}
	if len(fe.Calls()) != 1 {
		t.Fatalf("engine calls = %d, want 1 (no retry at scale)", len(fe.Calls()))
	}
	want := marshalGolden(t, byIdx)

	if *update {
		if err := os.WriteFile(goldenPath, want, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", goldenPath, err)
		}
		t.Logf("updated golden %s (%d segments)", goldenPath, len(byIdx))
		return
	}

	got, err := os.ReadFile(goldenPath)
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("golden %s missing; regenerate with: go test ./eval/diarize -run TestDiarizeScaleGolden -update", goldenPath)
	}
	if err != nil {
		t.Fatalf("read golden %s: %v", goldenPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("golden drift at scale. Produced grouping no longer matches %s.\n"+
			"If this change is intended, regenerate deliberately:\n"+
			"  go test ./eval/diarize -run TestDiarizeScaleGolden -update", goldenPath)
	}
}

// declaresLLM reports whether the language declares an llm engine slot (only such
// languages are diarizable).
func declaresLLM(l lang.Language) bool {
	for _, k := range l.EngineKeys() {
		if k == lang.EngineLLM {
			return true
		}
	}
	return false
}

// marshalGolden serializes the produced grouping in idx order — a stable byte
// form independent of map iteration order.
func marshalGolden(t *testing.T, byIdx map[int]string) []byte {
	t.Helper()
	out := make([]goldenAssignment, 0, len(byIdx))
	for idx, key := range byIdx {
		out = append(out, goldenAssignment{Idx: idx, SpeakerKey: key})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Idx < out[j].Idx })
	b, err := json.MarshalIndent(out, "", "  ")
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

// loadFile reads a raw fixture file (the recorded model response).
func loadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return b
}
