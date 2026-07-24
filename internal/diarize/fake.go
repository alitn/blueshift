package diarize

// fake.go bundles the deterministic grouping recording the offline/demo LLM
// wiring replays — the diarize counterpart of internal/asr's embed.go. The
// fixture is the model OUTPUT for the seeded demo sample's transcript (the
// embedded ASR fa recording: two turns, host then guest), hand-checked and
// committed, so `make demo`/`make dev`/CI drive the diarize stage through the
// REAL llm.Client validate/retry/audit loop with zero cost, no credential, and
// byte-stable results. cmd/worker feeds it to an llm.FakeEngine when
// LLM_ENGINE_MODE=fake; no provider is named anywhere in this path.

import (
	_ "embed"
)

// defaultFakeGrouping is the committed grouping recording. It assigns the demo
// sample's two segments to two distinct speakers (S1 the host, S2 the guest),
// matching the embedded ASR fixture fa_interview_open — the pairing is proven by
// TestDefaultFakeGroupingMatchesDemoTranscript, so the two fixtures cannot drift
// apart silently.
//
//go:embed fixtures/fa_interview_open.json
var defaultFakeGrouping []byte

// DefaultFakeGroupingResponse returns a copy of the committed deterministic
// grouping recording, the output an offline FakeEngine replays for the diarize
// stage. Callers get a fresh copy so no one can mutate the embedded bytes.
func DefaultFakeGroupingResponse() []byte {
	return append([]byte(nil), defaultFakeGrouping...)
}
