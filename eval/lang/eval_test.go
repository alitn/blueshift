package langeval

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"blueshift/internal/lang"

	// Register the content languages under evaluation. The registry is the only
	// source of which languages exist; the eval never hardcodes a language code.
	_ "blueshift/internal/lang/fa"
)

// update, when set, regenerates the golden files instead of comparing against
// them. It is the sole, explicit way goldens change — never a side effect of a
// passing run. Run: go test ./eval/lang -run TestNormalizationGolden -update
var update = flag.Bool("update", false, "regenerate eval goldens")

// corpusCase is one authored input in a language's testdata/<code>/corpus.json.
type corpusCase struct {
	Name string `json:"name"`
	Note string `json:"note,omitempty"`
	In   string `json:"in"`
}

// goldenCase is one entry in testdata/<code>/golden.json: the input paired with
// its normalized output. Generated from the corpus; never hand-edited.
type goldenCase struct {
	Name string `json:"name"`
	Note string `json:"note,omitempty"`
	In   string `json:"in"`
	Out  string `json:"out"`
}

// TestNormalizationGolden runs, for every registered language, its input corpus
// through Normalize and compares the result to the committed golden. It fails
// closed: a missing golden, a drift in normalization output, or a changed corpus
// all fail unless -update is passed to deliberately regenerate.
func TestNormalizationGolden(t *testing.T) {
	codes := lang.Registered()
	if len(codes) == 0 {
		t.Fatal("no languages registered; eval would be a no-op")
	}

	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			l, err := lang.Get(code)
			if err != nil {
				t.Fatalf("Get(%q): %v", code, err)
			}

			corpusPath := filepath.Join("testdata", code, "corpus.json")
			goldenPath := filepath.Join("testdata", code, "golden.json")

			corpus := loadCorpus(t, corpusPath)
			if len(corpus) == 0 {
				t.Fatalf("registered language %q has an empty/absent corpus at %s", code, corpusPath)
			}

			golden := make([]goldenCase, len(corpus))
			for i, c := range corpus {
				out := l.Normalize(c.In)
				// Idempotency is the core property this golden guards.
				if again := l.Normalize(out); again != out {
					t.Errorf("%s/%s: Normalize not idempotent: %q -> %q -> %q", code, c.Name, c.In, out, again)
				}
				golden[i] = goldenCase{Name: c.Name, Note: c.Note, In: c.In, Out: out}
			}

			want := mustMarshal(t, golden)

			if *update {
				if err := os.WriteFile(goldenPath, want, 0o644); err != nil {
					t.Fatalf("write golden %s: %v", goldenPath, err)
				}
				t.Logf("updated golden %s (%d cases)", goldenPath, len(golden))
				return
			}

			got, err := os.ReadFile(goldenPath)
			if errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("golden %s missing; regenerate with: go test ./eval/lang -run TestNormalizationGolden -update", goldenPath)
			}
			if err != nil {
				t.Fatalf("read golden %s: %v", goldenPath, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("golden drift for %q. Output no longer matches %s.\n"+
					"If this change is intended, regenerate deliberately:\n"+
					"  go test ./eval/lang -run TestNormalizationGolden -update", code, goldenPath)
			}
		})
	}
}

func loadCorpus(t *testing.T, path string) []corpusCase {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read corpus %s: %v", path, err)
	}
	var cases []corpusCase
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cases); err != nil {
		t.Fatalf("parse corpus %s: %v", path, err)
	}
	return cases
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	return append(b, '\n')
}
