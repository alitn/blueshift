package llm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// forbidden is the vendor-leak name list (mirrors the Makefile gate). These test
// files live in /internal/llm, which the gate does not scan, so we can name the
// providers here to assert their ABSENCE from anything a caller could see.
var forbidden = []string{
	"chirp", "gemini", "vertex", "google", "speech-to-text",
	"anthropic", "claude", "elevenlabs", "openai", "whisper",
	"deepgram", "assemblyai", "generativelanguage", "aiplatform",
}

// assertNeutral fails if err's message names any provider. The RETURNED error
// must always be neutral even though internal error strings and audit rows may
// legitimately carry provider detail.
func assertNeutral(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	msg := strings.ToLower(err.Error())
	for _, name := range forbidden {
		if strings.Contains(msg, name) {
			t.Errorf("error message %q leaks provider name %q", err.Error(), name)
		}
	}
}

// assertNoLeak fails if s names any provider.
func assertNoLeak(t *testing.T, what, s string) {
	t.Helper()
	low := strings.ToLower(s)
	for _, name := range forbidden {
		if strings.Contains(low, name) {
			t.Errorf("%s leaks provider name %q: %q", what, name, s)
		}
	}
}

// sampleOut is the caller's target struct for tests. DisallowUnknownFields makes
// any extra key in the model output a strict-decode failure.
type sampleOut struct {
	Answer string `json:"answer"`
	Count  int    `json:"count"`
}

// sampleSchema is a minimal provider-agnostic JSON schema matching sampleOut.
var sampleSchema = []byte(`{"type":"object","properties":{"answer":{"type":"string"},"count":{"type":"integer"}},"required":["answer","count"]}`)

// discardLogger is a logger that drops output (tests assert on state, not logs).
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// steppedClock returns a monotonic clock advancing by step on each call, so
// per-attempt latency is deterministic (exactly two now() calls per attempt).
func steppedClock(step time.Duration) func() time.Time {
	base := time.Unix(0, 0).UTC()
	var n int64
	return func() time.Time {
		t := base.Add(time.Duration(n) * step)
		n++
		return t
	}
}

// loadFixture reads a recorded provider response from testdata.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// fakeStep is one scripted engine outcome.
type fakeStep struct {
	res result
	err error
}

// fakeEngine is a scriptable engine for exercising the Client's retry/audit loop
// without HTTP. Each generate call consumes the next scripted step.
type fakeEngine struct {
	lbl   string
	mdl   string
	steps []fakeStep
	calls int
}

func (f *fakeEngine) label() string { return f.lbl }
func (f *fakeEngine) model() string { return f.mdl }

func (f *fakeEngine) generate(_ context.Context, _ call) (result, error) {
	i := f.calls
	f.calls++
	if i >= len(f.steps) {
		return result{}, errors.New("fakeEngine: no scripted step")
	}
	return f.steps[i].res, f.steps[i].err
}

// okStep builds a successful step whose extracted output is the given JSON and
// whose usage/rawBody are set for cost + audit assertions.
func okStep(output string, in, out int) fakeStep {
	return fakeStep{res: result{
		rawBody: []byte(`{"raw":"` + output + `"}`),
		output:  []byte(output),
		usage:   usage{inputTokens: in, outputTokens: out},
	}}
}

// truncatedStep builds a BILLABLE step the provider cut off at the output-token
// budget: a valid envelope + usage, truncated=true, and a partial output that
// must never be decoded. It exercises the Client's ErrTruncated branch without
// HTTP.
func truncatedStep(partial string, in, out int) fakeStep {
	return fakeStep{res: result{
		rawBody:   []byte(`{"finishReason":"MAX_TOKENS"}`),
		output:    []byte(partial),
		usage:     usage{inputTokens: in, outputTokens: out},
		truncated: true,
	}}
}

// errStep builds a failed (transport/non-2xx) step, optionally carrying a body.
func errStep(rawBody string) fakeStep {
	var raw []byte
	if rawBody != "" {
		raw = []byte(rawBody)
	}
	return fakeStep{res: result{rawBody: raw}, err: errors.New("upstream boom")}
}

// memAuditor records CallRecords in memory. auditErr, when set, is returned by
// RecordLLMCall to exercise the best-effort audit path.
type memAuditor struct {
	rows     []CallRecord
	auditErr error
}

func (m *memAuditor) RecordLLMCall(_ context.Context, rec CallRecord) error {
	m.rows = append(m.rows, rec)
	return m.auditErr
}

// newTestClient wires a Client around one fake engine + in-memory auditor.
func newTestClient(eng engine, price *Price, aud Auditor) *Client {
	return &Client{
		reg:   map[string]registered{eng.label(): {eng: eng, price: price}},
		audit: aud,
		log:   discardLogger(),
		now:   steppedClock(5 * time.Millisecond),
	}
}

// baseRequest is a valid Request template for orchestration tests.
func baseRequest(out *sampleOut) Request {
	return Request{
		Engine:        "bs-lm-1",
		PromptID:      "test.prompt",
		PromptVersion: "v1",
		System:        "be terse",
		Parts:         []string{"what is the capital of Portugal?"},
		Schema:        sampleSchema,
		Temperature:   0.2,
		MaxTokens:     256,
		OrgID:         1,
		EpisodeID:     7,
		Out:           out,
	}
}
