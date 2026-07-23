package asr

// fake.go is the deterministic, offline Engine used by tests and `make demo`. It
// returns transcripts recorded as JSON fixtures instead of calling any provider,
// so every flow that needs ASR output can run fully offline and reproducibly. It
// names no provider: it is a plain replayer of hand-checked recordings.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// errNoFixture means the FakeEngine has no recorded transcript for a request
// (neither an exact AudioKey match nor a fixture for the request's language). It
// is deterministic and explicit — the fake never fabricates output.
var errNoFixture = errors.New("asr: fake engine has no recorded fixture")

// fixtureFile is the on-disk shape of one recorded transcript under testdata. A
// fixture serves a request when its AudioKey is in MatchKeys, or (as a fallback)
// when its Language matches the request's language. Description is an internal
// note for maintainers and is never returned to callers.
type fixtureFile struct {
	Description string          `json:"description,omitempty"`
	Language    string          `json:"language"`
	MatchKeys   []string        `json:"match_keys,omitempty"`
	Segments    []Segment       `json:"segments"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

// FakeEngine replays recorded transcripts. It is immutable after construction and
// safe for concurrent use.
type FakeEngine struct {
	lbl    string
	byKey  map[string]*fixtureFile // exact AudioKey -> fixture
	byLang map[string]*fixtureFile // canonical primary subtag -> fixture (fallback)
}

// NewFakeEngine builds a FakeEngine labelled label, loading every "*.json"
// fixture in the root of fsys (e.g. os.DirFS("testdata")). Each fixture is
// strict-decoded and its transcript is Validate-checked at load time, so a
// malformed recording fails fast at startup rather than at request time. Fixtures
// are indexed by their MatchKeys (exact-key lookup) and by Language (first
// fixture per language wins, in sorted filename order, keeping resolution
// deterministic).
func NewFakeEngine(label string, fsys fs.FS) (*FakeEngine, error) {
	if label == "" {
		return nil, fmt.Errorf("asr: fake engine label is required")
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("asr: read fixtures: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names) // deterministic first-wins order for the language fallback

	f := &FakeEngine{
		lbl:    label,
		byKey:  make(map[string]*fixtureFile),
		byLang: make(map[string]*fixtureFile),
	}
	for _, name := range names {
		fx, err := loadFixture(fsys, name)
		if err != nil {
			return nil, err
		}
		for _, k := range fx.MatchKeys {
			if _, dup := f.byKey[k]; dup {
				return nil, fmt.Errorf("asr: fixture %s: duplicate match key %q", name, k)
			}
			f.byKey[k] = fx
		}
		if lang := primarySubtag(fx.Language); lang != "" {
			if _, present := f.byLang[lang]; !present {
				f.byLang[lang] = fx
			}
		}
	}
	if len(f.byKey) == 0 && len(f.byLang) == 0 {
		return nil, fmt.Errorf("asr: no fixtures found")
	}
	return f, nil
}

// loadFixture reads and validates one fixture file.
func loadFixture(fsys fs.FS, name string) (*fixtureFile, error) {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("asr: read fixture %s: %w", name, err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var fx fixtureFile
	if err := dec.Decode(&fx); err != nil {
		return nil, fmt.Errorf("asr: decode fixture %s: %w", name, err)
	}
	if fx.Language == "" {
		return nil, fmt.Errorf("asr: fixture %s: language is required", name)
	}
	// A fixture is only useful if it is a valid transcript; reject a bad recording
	// at load time so the fake can never replay malformed timing.
	tr := Transcript{Engine: "", Language: fx.Language, Segments: fx.Segments}
	if err := tr.Validate(); err != nil {
		return nil, fmt.Errorf("asr: fixture %s: %w", name, err)
	}
	return &fx, nil
}

// Label returns the neutral engine label the fake answers to.
func (f *FakeEngine) Label() string { return f.lbl }

// Transcribe returns the recorded transcript matching req, deep-copied so callers
// cannot mutate the shared fixture. Resolution is exact AudioKey first, then the
// request's language; a request matching no fixture returns errNoFixture. Bias
// terms and options are ignored — the fake is a deterministic replayer, so the
// same request always yields the same transcript.
func (f *FakeEngine) Transcribe(ctx context.Context, req TranscribeRequest) (Transcript, error) {
	if err := ctx.Err(); err != nil {
		return Transcript{}, err
	}
	fx := f.resolve(req)
	if fx == nil {
		return Transcript{}, fmt.Errorf("%w (key=%q lang=%q)", errNoFixture, req.AudioKey, req.Language)
	}
	return Transcript{
		Engine:   f.lbl,
		Language: req.Language,
		Segments: cloneSegments(fx.Segments),
		Raw:      append(json.RawMessage(nil), fx.Raw...),
	}, nil
}

// resolve picks the fixture for req: an exact AudioKey match, else a fixture for
// the request's primary language subtag, else nil.
func (f *FakeEngine) resolve(req TranscribeRequest) *fixtureFile {
	if fx, ok := f.byKey[req.AudioKey]; ok {
		return fx
	}
	if fx, ok := f.byLang[primarySubtag(req.Language)]; ok {
		return fx
	}
	return nil
}

// cloneSegments returns a deep copy of segs so a returned Transcript shares no
// backing storage with the cached fixture (Word is a value type, so copying the
// Words slice is a full copy).
func cloneSegments(segs []Segment) []Segment {
	if segs == nil {
		return nil
	}
	out := make([]Segment, len(segs))
	for i, s := range segs {
		out[i] = s
		if s.Words != nil {
			w := make([]Word, len(s.Words))
			copy(w, s.Words)
			out[i].Words = w
		}
	}
	return out
}

// primarySubtag reduces a BCP-47 tag to its lowercased primary language subtag
// ("fa-IR" -> "fa"), the granularity fixtures are indexed by. It is a small local
// helper so the fake stays decoupled from the lang registry (it is a pure
// replayer and does no language resolution).
func primarySubtag(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	tag = strings.ReplaceAll(tag, "_", "-")
	if i := strings.IndexByte(tag, '-'); i > 0 {
		return tag[:i]
	}
	return tag
}
