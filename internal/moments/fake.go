package moments

// fake.go bundles the deterministic proposal recording the offline/demo LLM
// wiring replays — the moments counterpart of internal/diarize's fake.go. The
// fixture is the model OUTPUT for the seeded demo sample's transcript (the
// embedded ASR fa recording after the deterministic resegmentation: two turns,
// host then guest), hand-checked and committed, so `make demo`/`make dev`/CI
// drive the moments stage through the REAL llm.Client validate/retry/audit
// loop with zero cost, no credential, and byte-stable results. cmd/worker
// feeds it to an llm.FakeEngine when LLM_ENGINE_MODE=fake; no provider is
// named anywhere in this path.

import (
	_ "embed"
)

// defaultFakeSelection is the committed proposal recording. It ranks the demo
// sample's two segments as two single-segment moments (the guest reply first,
// the host greeting second), each quote a verbatim substring of its segment's
// text — the pairing is proven by TestDefaultFakeSelectionMatchesDemoTranscript,
// so the two fixtures cannot drift apart silently.
//
//go:embed fixtures/fa_interview_open.json
var defaultFakeSelection []byte

// DefaultFakeSelectionResponse returns a copy of the committed deterministic
// proposal recording, the output an offline FakeEngine replays for the moments
// stage. Callers get a fresh copy so no one can mutate the embedded bytes.
func DefaultFakeSelectionResponse() []byte {
	return append([]byte(nil), defaultFakeSelection...)
}
