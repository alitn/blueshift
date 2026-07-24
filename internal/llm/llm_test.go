package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func newFake() *fakeEngine { return &fakeEngine{lbl: "bs-lm-1", mdl: "test-model-x"} }

// TestGenerateSuccess: a single valid call decodes into Out, returns the cost
// from configured price, and writes exactly one 'ok' audit row.
func TestGenerateSuccess(t *testing.T) {
	fe := newFake()
	fe.steps = []fakeStep{okStep(`{"answer":"Lisbon","count":2}`, 1_200_000, 800_000)}
	aud := &memAuditor{}
	c := newTestClient(fe, &Price{InputPerMTokCents: 100, OutputPerMTokCents: 300}, aud)

	var out sampleOut
	resp, err := c.Generate(context.Background(), baseRequest(&out))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if out.Answer != "Lisbon" || out.Count != 2 {
		t.Errorf("decoded Out = %+v", out)
	}
	if resp.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", resp.Attempts)
	}
	if resp.CostCents == nil || *resp.CostCents != 360 {
		t.Errorf("CostCents = %v, want 360", resp.CostCents)
	}
	if resp.LatencyMS != 5 {
		t.Errorf("LatencyMS = %d, want 5", resp.LatencyMS)
	}
	if len(aud.rows) != 1 || aud.rows[0].Status != statusOK {
		t.Fatalf("audit rows = %+v, want one ok row", aud.rows)
	}
	if aud.rows[0].Model != "test-model-x" || aud.rows[0].PromptVersion != "v1" {
		t.Errorf("audit row model/version = %q/%q", aud.rows[0].Model, aud.rows[0].PromptVersion)
	}
	if aud.rows[0].CostCents == nil || *aud.rows[0].CostCents != 360 {
		t.Errorf("audit cost = %v, want 360", aud.rows[0].CostCents)
	}
	if aud.rows[0].OrgID != 1 || aud.rows[0].EpisodeID != 7 {
		t.Errorf("audit scoping = org %d ep %d", aud.rows[0].OrgID, aud.rows[0].EpisodeID)
	}
}

// TestGenerateInvalidThenSuccess: an unknown-field output fails strict decode,
// the single retry succeeds. Two rows: invalid then ok.
func TestGenerateInvalidThenSuccess(t *testing.T) {
	fe := newFake()
	fe.steps = []fakeStep{
		okStep(`{"answer":"x","count":1,"surprise":true}`, 10, 5), // unknown field -> invalid
		okStep(`{"answer":"Lisbon","count":2}`, 10, 5),
	}
	aud := &memAuditor{}
	c := newTestClient(fe, &Price{InputPerMTokCents: 100, OutputPerMTokCents: 100}, aud)

	var out sampleOut
	resp, err := c.Generate(context.Background(), baseRequest(&out))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", resp.Attempts)
	}
	if fe.calls != 2 {
		t.Errorf("engine calls = %d, want 2 (one retry)", fe.calls)
	}
	if len(aud.rows) != 2 {
		t.Fatalf("audit rows = %d, want 2", len(aud.rows))
	}
	if aud.rows[0].Status != statusInvalid || aud.rows[1].Status != statusOK {
		t.Errorf("statuses = %q,%q want invalid,ok", aud.rows[0].Status, aud.rows[1].Status)
	}
	// The invalid attempt is still billable and its input_hash matches the retry.
	if aud.rows[0].CostCents == nil {
		t.Error("invalid attempt should still record a cost")
	}
	if aud.rows[0].InputHash != aud.rows[1].InputHash {
		t.Error("retry must reuse the same input_hash")
	}
}

// TestGenerateInvalidTwice: two invalid outputs -> hard fail ErrInvalidOutput,
// two 'invalid' rows, neutral error carrying an internal id.
func TestGenerateInvalidTwice(t *testing.T) {
	fe := newFake()
	bad := okStep(`{"answer":"x","count":1,"surprise":true}`, 10, 5)
	fe.steps = []fakeStep{bad, bad}
	aud := &memAuditor{}
	c := newTestClient(fe, &Price{InputPerMTokCents: 1, OutputPerMTokCents: 1}, aud)

	var out sampleOut
	_, err := c.Generate(context.Background(), baseRequest(&out))
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("err = %v, want ErrInvalidOutput", err)
	}
	assertNeutral(t, err)
	if !strings.Contains(err.Error(), "[") {
		t.Errorf("hard-fail error should carry an internal error id, got %q", err.Error())
	}
	if fe.calls != 2 {
		t.Errorf("engine calls = %d, want exactly 2 (one retry)", fe.calls)
	}
	if len(aud.rows) != 2 || aud.rows[0].Status != statusInvalid || aud.rows[1].Status != statusInvalid {
		t.Fatalf("audit rows = %+v, want two invalid rows", aud.rows)
	}
}

// TestGenerateNeverExceedsMaxAttempts is the LLM half of the cost-safety
// bounded-retries audit (CLAUDE.md "Billable-service cost safety", item 3): even
// when every attempt fails and the engine could keep answering, the Client makes at
// MOST maxAttempts billable provider calls (one initial + one retry) and then
// hard-fails. Scripting MORE failing steps than that proves the ceiling is enforced
// by the Client, not an artefact of running out of scripted steps — there is no
// unbounded billable loop.
func TestGenerateNeverExceedsMaxAttempts(t *testing.T) {
	if maxAttempts != 2 {
		t.Fatalf("maxAttempts = %d, want 2 (CLAUDE.md: invalid -> one retry -> hard fail)", maxAttempts)
	}
	fe := newFake()
	// Five identical invalid outputs are available; the Client must consume only two.
	bad := okStep(`{"answer":"x","count":1,"surprise":true}`, 10, 5)
	fe.steps = []fakeStep{bad, bad, bad, bad, bad}
	c := newTestClient(fe, nil, &memAuditor{})

	var out sampleOut
	if _, err := c.Generate(context.Background(), baseRequest(&out)); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("err = %v, want ErrInvalidOutput", err)
	}
	if fe.calls != maxAttempts {
		t.Errorf("engine calls = %d, want exactly maxAttempts=%d (bounded retries; no unbounded billable loop)", fe.calls, maxAttempts)
	}
}

// TestGenerateTransportErrorTwice: two upstream failures -> ErrUnavailable, two
// 'error' rows (cost NULL), neutral error.
func TestGenerateTransportErrorTwice(t *testing.T) {
	fe := newFake()
	fe.steps = []fakeStep{errStep(`{"error":"vertex ai down at googleapis.com"}`), errStep("")}
	aud := &memAuditor{}
	c := newTestClient(fe, &Price{InputPerMTokCents: 1, OutputPerMTokCents: 1}, aud)

	var out sampleOut
	_, err := c.Generate(context.Background(), baseRequest(&out))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	assertNeutral(t, err)
	if len(aud.rows) != 2 {
		t.Fatalf("audit rows = %d, want 2", len(aud.rows))
	}
	for i, r := range aud.rows {
		if r.Status != statusError {
			t.Errorf("row %d status = %q, want error", i, r.Status)
		}
		if r.CostCents != nil {
			t.Errorf("row %d cost = %v, want nil on error", i, r.CostCents)
		}
	}
	// The first error body is stored verbatim for the audit even though the
	// returned error is neutral.
	if len(aud.rows[0].RawResponse) == 0 {
		t.Error("error attempt should still capture the response body for audit")
	}
}

// TestGenerateFirstErrorThenSuccess: a transport failure then a clean call.
func TestGenerateFirstErrorThenSuccess(t *testing.T) {
	fe := newFake()
	fe.steps = []fakeStep{errStep(""), okStep(`{"answer":"Lisbon","count":2}`, 10, 5)}
	aud := &memAuditor{}
	c := newTestClient(fe, &Price{InputPerMTokCents: 1, OutputPerMTokCents: 1}, aud)

	var out sampleOut
	resp, err := c.Generate(context.Background(), baseRequest(&out))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", resp.Attempts)
	}
	if aud.rows[0].Status != statusError || aud.rows[1].Status != statusOK {
		t.Errorf("statuses = %q,%q want error,ok", aud.rows[0].Status, aud.rows[1].Status)
	}
}

// TestGenerateValidateHook: a semantic hook failure is treated as invalid.
func TestGenerateValidateHook(t *testing.T) {
	fe := newFake()
	fe.steps = []fakeStep{
		okStep(`{"answer":"Lisbon","count":2}`, 10, 5),
		okStep(`{"answer":"Lisbon","count":2}`, 10, 5),
	}
	aud := &memAuditor{}
	c := newTestClient(fe, nil, aud)

	var out sampleOut
	req := baseRequest(&out)
	req.Validate = func() error { return errors.New("answer failed a business rule") }
	_, err := c.Generate(context.Background(), req)
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("err = %v, want ErrInvalidOutput", err)
	}
	if len(aud.rows) != 2 || aud.rows[1].Status != statusInvalid {
		t.Errorf("audit rows = %+v, want two invalid rows", aud.rows)
	}
}

// TestGenerateUnknownPriceNilCost: no configured price -> cost NULL on every row.
func TestGenerateUnknownPriceNilCost(t *testing.T) {
	fe := newFake()
	fe.steps = []fakeStep{okStep(`{"answer":"Lisbon","count":2}`, 10, 5)}
	aud := &memAuditor{}
	c := newTestClient(fe, nil, aud) // nil price

	var out sampleOut
	resp, err := c.Generate(context.Background(), baseRequest(&out))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.CostCents != nil {
		t.Errorf("CostCents = %v, want nil (unknown price)", resp.CostCents)
	}
	if aud.rows[0].CostCents != nil {
		t.Errorf("audit cost = %v, want nil (unknown price)", aud.rows[0].CostCents)
	}
}

// TestGenerateUnknownEngine: unregistered label -> ErrUnknownEngine, no audit.
func TestGenerateUnknownEngine(t *testing.T) {
	fe := newFake()
	aud := &memAuditor{}
	c := newTestClient(fe, nil, aud)

	var out sampleOut
	req := baseRequest(&out)
	req.Engine = "bs-lm-nope"
	_, err := c.Generate(context.Background(), req)
	if !errors.Is(err, ErrUnknownEngine) {
		t.Fatalf("err = %v, want ErrUnknownEngine", err)
	}
	assertNeutral(t, err)
	if len(aud.rows) != 0 {
		t.Errorf("audit rows = %d, want 0 (no call made)", len(aud.rows))
	}
}

// TestGenerateBadRequest: missing required fields -> ErrBadRequest, no audit,
// no engine call.
func TestGenerateBadRequest(t *testing.T) {
	fe := newFake()
	aud := &memAuditor{}
	c := newTestClient(fe, nil, aud)

	cases := map[string]func(*Request){
		"no engine": func(r *Request) { r.Engine = "" },
		"no org":    func(r *Request) { r.OrgID = 0 },
		"no schema": func(r *Request) { r.Schema = nil },
		"no out":    func(r *Request) { r.Out = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var out sampleOut
			req := baseRequest(&out)
			mutate(&req)
			_, err := c.Generate(context.Background(), req)
			if !errors.Is(err, ErrBadRequest) {
				t.Fatalf("err = %v, want ErrBadRequest", err)
			}
			assertNeutral(t, err)
		})
	}
	if fe.calls != 0 {
		t.Errorf("engine calls = %d, want 0 for bad requests", fe.calls)
	}
	if len(aud.rows) != 0 {
		t.Errorf("audit rows = %d, want 0 for bad requests", len(aud.rows))
	}
}

// TestGenerateContextCancelled: a cancelled context stops before another attempt
// and returns the context error, not a retry.
func TestGenerateContextCancelled(t *testing.T) {
	fe := newFake()
	fe.steps = []fakeStep{okStep(`{"answer":"Lisbon","count":2}`, 10, 5)}
	aud := &memAuditor{}
	c := newTestClient(fe, nil, aud)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out sampleOut
	_, err := c.Generate(ctx, baseRequest(&out))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if fe.calls != 0 {
		t.Errorf("engine calls = %d, want 0 when context already cancelled", fe.calls)
	}
}

// TestGenerateAuditFailureDoesNotFailResult: a broken audit sink logs but does
// not turn a good call into a caller-visible failure.
func TestGenerateAuditFailureDoesNotFailResult(t *testing.T) {
	fe := newFake()
	fe.steps = []fakeStep{okStep(`{"answer":"Lisbon","count":2}`, 10, 5)}
	aud := &memAuditor{auditErr: errors.New("db down")}
	c := newTestClient(fe, nil, aud)

	var out sampleOut
	if _, err := c.Generate(context.Background(), baseRequest(&out)); err != nil {
		t.Fatalf("Generate should succeed despite audit failure: %v", err)
	}
}

// TestInputHashStability: identical inputs hash identically; a schema that
// differs only in whitespace/key-order hashes identically; a changed control
// (temperature) changes the hash.
func TestInputHashStability(t *testing.T) {
	run := func(mutate func(*Request)) string {
		fe := newFake()
		fe.steps = []fakeStep{okStep(`{"answer":"Lisbon","count":2}`, 10, 5)}
		aud := &memAuditor{}
		c := newTestClient(fe, nil, aud)
		var out sampleOut
		req := baseRequest(&out)
		if mutate != nil {
			mutate(&req)
		}
		if _, err := c.Generate(context.Background(), req); err != nil {
			t.Fatalf("Generate: %v", err)
		}
		return aud.rows[0].InputHash
	}

	h1 := run(nil)
	h2 := run(nil)
	if h1 != h2 {
		t.Errorf("identical inputs hashed differently: %s vs %s", h1, h2)
	}

	hReordered := run(func(r *Request) {
		// Same schema value, different key order and whitespace.
		r.Schema = []byte("{\n  \"required\": [\"answer\", \"count\"],\n  \"properties\": {\"count\": {\"type\": \"integer\"}, \"answer\": {\"type\": \"string\"}},\n  \"type\": \"object\"\n}")
	})
	if hReordered != h1 {
		t.Errorf("cosmetically different schema changed the hash: %s vs %s", hReordered, h1)
	}

	hTemp := run(func(r *Request) { r.Temperature = 0.9 })
	if hTemp == h1 {
		t.Error("changing temperature must change the input_hash")
	}
}

// TestNewValidation exercises the config-driven constructor's fail-fast rules.
func TestNewValidation(t *testing.T) {
	aud := &memAuditor{}
	tok := func(context.Context) (string, error) { return "t", nil }

	if _, err := New(Options{Auditor: nil, Engines: []EngineConfig{{Label: "x", Provider: ProviderClaude, Model: "m"}}}); err == nil {
		t.Error("want error when auditor is nil")
	}
	if _, err := New(Options{Auditor: aud}); err == nil {
		t.Error("want error when no engines configured")
	}
	if _, err := New(Options{Auditor: aud, Engines: []EngineConfig{
		{Label: "dup", Provider: ProviderClaude, Model: "m"},
		{Label: "dup", Provider: ProviderClaude, Model: "m"},
	}, Claude: ClaudeOptions{APIKey: "k"}}); err == nil {
		t.Error("want error on duplicate label")
	}
	if _, err := New(Options{Auditor: aud, Engines: []EngineConfig{{Label: "x", Provider: "made-up", Model: "m"}}}); err == nil {
		t.Error("want error on unknown provider")
	}
	if _, err := New(Options{Auditor: aud, Engines: []EngineConfig{{Label: "x", Provider: ProviderGemini, Model: "m"}}}); err == nil {
		t.Error("want error when gemini has neither endpoint nor project+region")
	}

	// A well-formed multi-engine config builds cleanly.
	c, err := New(Options{
		Auditor: aud,
		Logger:  discardLogger(),
		Engines: []EngineConfig{
			{Label: "bs-lm-1", Provider: ProviderGemini, Model: "m-a", Price: &Price{InputPerMTokCents: 10}},
			{Label: "bs-lm-2", Provider: ProviderClaude, Model: "m-b"},
		},
		Gemini: GeminiOptions{Endpoint: "https://example.invalid/models", Token: tok},
		Claude: ClaudeOptions{APIKey: "k"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(c.reg) != 2 {
		t.Errorf("registry size = %d, want 2", len(c.reg))
	}
}
