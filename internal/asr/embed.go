package asr

// embed.go bundles the FakeEngine's default offline recordings into the binary so
// the worker can transcribe with the fake engine in `make demo`/`make dev` (and
// in staging with ASR_ENGINE_MODE=fake) without a fixtures path on disk. The
// recordings are hand-checked, provider-free JSON — a plain replayer's data, not
// a provider call. Tests construct their own FakeEngine from case-specific
// fixtures; this is only the runtime default set.

import (
	"embed"
	"fmt"
	"io/fs"
)

// fakeFixturesFS holds the committed default fixtures. Each is loaded and
// Validate-checked at construction (NewFakeEngine), so a malformed recording
// fails the worker fast at startup rather than at request time.
//
//go:embed fixtures/*.json
var fakeFixturesFS embed.FS

// NewDefaultFakeEngine builds a FakeEngine, labelled label, from the embedded
// default recordings. It is the offline/demo ASR wiring: the worker registers it
// under the neutral label and the transcribe stage drives it exactly as it would
// a provider-backed engine, so every flow runs deterministically with no
// credential or network.
func NewDefaultFakeEngine(label string) (*FakeEngine, error) {
	sub, err := fs.Sub(fakeFixturesFS, "fixtures")
	if err != nil {
		return nil, fmt.Errorf("asr: open embedded fixtures: %w", err)
	}
	return NewFakeEngine(label, sub)
}
