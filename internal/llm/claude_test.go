package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestClaudeGenerateSuccess(t *testing.T) {
	var got capturedRequest
	srv := fixtureServer(t, http.StatusOK, loadFixture(t, "claude_success.json"), &got)
	defer srv.Close()

	e := &claudeEngine{
		lbl: "bs-lm-2", mdl: "test-model", base: srv.URL,
		apiKey: "secret-key", version: claudeDefaultVersion, hc: srv.Client(),
	}
	res, err := e.generate(context.Background(), call{
		system: "be terse", parts: []string{"capital of Portugal?"},
		schema: sampleSchema, temperature: 0.2, maxTokens: 256,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Wire shape: POST /messages, keyed + versioned, GA output_config.format.
	if got.method != http.MethodPost || !strings.HasSuffix(got.path, "/messages") {
		t.Errorf("method/path = %s %q, want POST .../messages", got.method, got.path)
	}
	if got.header.Get("x-api-key") != "secret-key" {
		t.Errorf("x-api-key = %q, want secret-key", got.header.Get("x-api-key"))
	}
	if got.header.Get("anthropic-version") != claudeDefaultVersion {
		t.Errorf("anthropic-version = %q, want %s", got.header.Get("anthropic-version"), claudeDefaultVersion)
	}
	var sent map[string]any
	if err := json.Unmarshal(got.body, &sent); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	oc, _ := sent["output_config"].(map[string]any)
	format, _ := oc["format"].(map[string]any)
	if format["type"] != "json_schema" {
		t.Errorf("output_config.format.type = %v, want json_schema", format["type"])
	}
	if _, ok := format["schema"].(map[string]any); !ok {
		t.Errorf("output_config.format.schema missing/!object: %v", format["schema"])
	}
	if sent["model"] != "test-model" {
		t.Errorf("model = %v, want test-model", sent["model"])
	}
	if sent["system"] != "be terse" {
		t.Errorf("system = %v, want 'be terse'", sent["system"])
	}

	// Extraction: output is content[0].text; usage from usage.{input,output}_tokens.
	var out sampleOut
	if err := json.Unmarshal(res.output, &out); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if out.Answer != "the capital is Lisbon" || out.Count != 2 {
		t.Errorf("extracted output = %+v", out)
	}
	if res.usage.inputTokens != 1_200_000 || res.usage.outputTokens != 800_000 {
		t.Errorf("usage = %+v", res.usage)
	}
}

// TestClaudeTruncationDetected proves the engine reads stop_reason max_tokens and
// returns a billable truncated result (no error), the Claude analogue of Gemini's
// MAX_TOKENS detection that the Client maps to the neutral ErrTruncated.
func TestClaudeTruncationDetected(t *testing.T) {
	srv := fixtureServer(t, http.StatusOK, loadFixture(t, "claude_truncated.json"), nil)
	defer srv.Close()

	e := &claudeEngine{lbl: "bs-lm-2", mdl: "m", base: srv.URL, apiKey: "k", version: claudeDefaultVersion, hc: srv.Client()}
	res, err := e.generate(context.Background(), call{parts: []string{"x"}, schema: sampleSchema, maxTokens: 8192})
	if err != nil {
		t.Fatalf("generate returned an error for a max_tokens response; want a truncated result: %v", err)
	}
	if !res.truncated {
		t.Error("res.truncated = false, want true for stop_reason max_tokens")
	}
	if res.usage.inputTokens != 13235 || res.usage.outputTokens != 316 {
		t.Errorf("usage = %+v, want input 13235 / output 316", res.usage)
	}
	// A normal end_turn fixture is NOT truncated.
	stopSrv := fixtureServer(t, http.StatusOK, loadFixture(t, "claude_success.json"), nil)
	defer stopSrv.Close()
	e.base = stopSrv.URL
	stopRes, err := e.generate(context.Background(), call{parts: []string{"x"}, schema: sampleSchema})
	if err != nil {
		t.Fatalf("generate (end_turn): %v", err)
	}
	if stopRes.truncated {
		t.Error("res.truncated = true for a stop_reason end_turn response, want false")
	}
}

func TestClaudeNon2xxNeutral(t *testing.T) {
	body := loadFixture(t, "claude_error.json")
	srv := fixtureServer(t, http.StatusUnauthorized, body, nil)
	defer srv.Close()

	e := &claudeEngine{lbl: "bs-lm-2", mdl: "m", base: srv.URL, apiKey: "k", version: claudeDefaultVersion, hc: srv.Client()}
	res, err := e.generate(context.Background(), call{parts: []string{"x"}, schema: sampleSchema})
	if err == nil {
		t.Fatal("want error on 401")
	}
	assertNeutral(t, err)
	if string(res.rawBody) != string(body) {
		t.Error("non-2xx body should be captured verbatim in rawBody for audit")
	}
}

// TestClaudeApiKeyNeverLeaks: even when the endpoint is unreachable (a transport
// error whose *url.Error embeds the request URL), the returned error must not
// contain the API key or a provider name.
func TestClaudeApiKeyNeverLeaks(t *testing.T) {
	const secret = "SUPER-SECRET-ANTHROPIC-KEY-xyz789"
	srv := fixtureServer(t, http.StatusOK, loadFixture(t, "claude_success.json"), nil)
	client := srv.Client()
	base := srv.URL
	srv.Close() // unreachable -> transport error

	e := &claudeEngine{lbl: "bs-lm-2", mdl: "m", base: base, apiKey: secret, version: claudeDefaultVersion, hc: client}
	_, err := e.generate(context.Background(), call{parts: []string{"x"}, schema: sampleSchema})
	if err == nil {
		t.Fatal("want transport error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks the API key: %q", err.Error())
	}
	assertNeutral(t, err)
}

func TestClaudeDefaultsAppliedByNew(t *testing.T) {
	// New should default the Claude endpoint/version when unset; a built client
	// must resolve the engine and use api version 2023-06-01.
	var got capturedRequest
	srv := fixtureServer(t, http.StatusOK, loadFixture(t, "claude_success.json"), &got)
	defer srv.Close()

	aud := &memAuditor{}
	c, err := New(Options{
		Auditor: aud, Logger: discardLogger(),
		Engines: []EngineConfig{{Label: "bs-lm-2", Provider: ProviderClaude, Model: "m", Price: &Price{}}},
		Claude:  ClaudeOptions{Endpoint: srv.URL, APIKey: "k"}, // Version left empty -> default
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var out sampleOut
	if _, err := c.Generate(context.Background(), Request{
		Engine: "bs-lm-2", PromptVersion: "v1", Parts: []string{"x"},
		Schema: sampleSchema, OrgID: 1, Out: &out,
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.header.Get("anthropic-version") != claudeDefaultVersion {
		t.Errorf("anthropic-version = %q, want default %s", got.header.Get("anthropic-version"), claudeDefaultVersion)
	}
}
