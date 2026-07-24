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

// defaultFakeCompose is the committed COMPOSE recording: the model output the
// offline app server replays for the free-prompt compose call. One
// single-segment result over the demo sample's guest reply, its quote a
// verbatim ZWNJ-carrying substring of segment 1's text — deliberately a
// one-moment set, which the stage validator would reject (below the clamped
// minimum) but the compose validator accepts, so demo/e2e prove the
// no-min-count contract through the REAL llm.Client validate loop. The
// pairing with the demo transcript is proven by
// TestDefaultFakeComposeMatchesDemoTranscript.
//
//go:embed fixtures/fa_compose_prompt.json
var defaultFakeCompose []byte

// DefaultFakeComposeResponse returns a copy of the committed deterministic
// compose recording, the output an offline FakeEngine replays for the
// free-prompt compose call. Callers get a fresh copy so no one can mutate the
// embedded bytes.
func DefaultFakeComposeResponse() []byte {
	return append([]byte(nil), defaultFakeCompose...)
}
