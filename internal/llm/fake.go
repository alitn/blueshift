package llm

// fake.go is the deterministic, offline engine used by tests, the diarization
// golden eval, and (later) `make demo` — the LLM counterpart of asr.FakeEngine.
// It replays a recorded model OUTPUT through the Client's real
// schema-validate / one-retry / audit loop instead of calling any provider, so
// every flow that needs a language model runs fully offline and reproducibly. It
// names no provider: it is a plain replayer of a hand-checked recording.
//
// Because a FakeEngine is driven only through a Client (the retry/audit loop is
// the Client's job, above the engine seam), the same code path a provider-backed
// engine takes is exercised — including the one-retry-then-hard-fail on invalid
// output. A FakeEngine returns the SAME recorded output on every call, so a
// recording that fails validation drives the retry naturally: attempt, retry,
// hard fail (both attempts see the same invalid output).

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"
)

// FakeEngine is an offline engine that returns one recorded output for every
// generate call. It implements the (unexported) engine interface, so it is only
// ever constructed into a Client via NewFakeClient. It records the calls it
// received (system + parts) so a test can assert exactly what crossed the seam —
// e.g. that a diarization request carried NO timestamps (text-anchoring).
type FakeEngine struct {
	lbl    string
	mdl    string
	output []byte
	raw    []byte
	use    usage

	mu    sync.Mutex
	calls []RecordedCall
}

// RecordedCall is one generate call a FakeEngine received, exposed for tests that
// assert what was actually sent to the model (Parts never contain timestamps for
// a text-anchored request).
type RecordedCall struct {
	System string
	Parts  []string
}

// FakeOption customises a FakeEngine.
type FakeOption func(*FakeEngine)

// WithFakeUsage sets the token usage reported for costing on each call.
func WithFakeUsage(inputTokens, outputTokens int) FakeOption {
	return func(f *FakeEngine) { f.use = usage{inputTokens: inputTokens, outputTokens: outputTokens} }
}

// NewFakeEngine builds a FakeEngine labelled label (a neutral engine label, e.g.
// "bs-lm-1") backed by model mdl (a neutral internal id for the audit) that
// returns output — the structured JSON the model "produces" — on every call.
func NewFakeEngine(label, mdl string, output []byte, opts ...FakeOption) *FakeEngine {
	f := &FakeEngine{
		lbl:    label,
		mdl:    mdl,
		output: append([]byte(nil), output...),
		raw:    append([]byte(nil), output...),
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

func (f *FakeEngine) label() string { return f.lbl }
func (f *FakeEngine) model() string { return f.mdl }

// generate records the call and returns the recorded output. It honours context
// cancellation (like a real engine) so a cancelled Generate stops without a
// fabricated answer.
func (f *FakeEngine) generate(ctx context.Context, c call) (result, error) {
	if err := ctx.Err(); err != nil {
		return result{}, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, RecordedCall{System: c.system, Parts: append([]string(nil), c.parts...)})
	f.mu.Unlock()
	return result{
		rawBody: append([]byte(nil), f.raw...),
		output:  append([]byte(nil), f.output...),
		usage:   f.use,
	}, nil
}

// Calls returns a snapshot of the generate calls this engine received, in order.
func (f *FakeEngine) Calls() []RecordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]RecordedCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// noopAuditor discards audit rows. It is the default sink for a fake Client whose
// caller does not care about the audit (e.g. the golden eval).
type noopAuditor struct{}

func (noopAuditor) RecordLLMCall(context.Context, CallRecord) error { return nil }

// NewFakeClient wires a Client around one or more FakeEngines and an auditor,
// exposing the full validate/retry/audit loop with no provider or network. A nil
// auditor defaults to a discarding sink; pass a capturing auditor to assert the
// llm_calls rows a call would write. Logs are discarded (a fake's retry WARNs are
// rarely wanted); the clock is the real time.Now.
func NewFakeClient(auditor Auditor, engines ...*FakeEngine) (*Client, error) {
	if len(engines) == 0 {
		return nil, errors.New("llm: NewFakeClient needs at least one engine")
	}
	if auditor == nil {
		auditor = noopAuditor{}
	}
	reg := make(map[string]registered, len(engines))
	for _, e := range engines {
		if e == nil || e.lbl == "" {
			return nil, errors.New("llm: NewFakeClient: engine label is required")
		}
		if _, dup := reg[e.lbl]; dup {
			return nil, errors.New("llm: NewFakeClient: duplicate engine label " + e.lbl)
		}
		reg[e.lbl] = registered{eng: e}
	}
	return &Client{
		reg:   reg,
		audit: auditor,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:   time.Now,
	}, nil
}
