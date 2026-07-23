package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capturedRequest records what an engine sent, for wire-shape assertions.
type capturedRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// fixtureServer serves body with status and captures the incoming request.
func fixtureServer(t *testing.T, status int, body []byte, got *capturedRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			b, _ := io.ReadAll(r.Body)
			got.method = r.Method
			got.path = r.URL.Path
			got.header = r.Header.Clone()
			got.body = b
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
}

func TestGeminiGenerateSuccess(t *testing.T) {
	var got capturedRequest
	srv := fixtureServer(t, http.StatusOK, loadFixture(t, "gemini_success.json"), &got)
	defer srv.Close()

	g := &geminiEngine{
		lbl:   "bs-lm-1",
		mdl:   "test-model",
		base:  srv.URL,
		token: func(context.Context) (string, error) { return "test-token", nil },
		hc:    srv.Client(),
	}
	res, err := g.generate(context.Background(), call{
		system: "be terse", parts: []string{"capital of Portugal?"},
		schema: sampleSchema, temperature: 0.2, maxTokens: 256,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Wire shape: POST to {model}:generateContent, bearer auth, JSON mode + schema.
	if got.method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.method)
	}
	if !strings.HasSuffix(got.path, "/test-model:generateContent") {
		t.Errorf("path = %q, want .../test-model:generateContent", got.path)
	}
	if got.header.Get("Authorization") != "Bearer test-token" {
		t.Errorf("Authorization = %q, want Bearer test-token", got.header.Get("Authorization"))
	}
	var sent map[string]any
	if err := json.Unmarshal(got.body, &sent); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	gc, _ := sent["generationConfig"].(map[string]any)
	if gc["responseMimeType"] != "application/json" {
		t.Errorf("responseMimeType = %v, want application/json", gc["responseMimeType"])
	}
	if _, ok := gc["responseSchema"].(map[string]any); !ok {
		t.Errorf("responseSchema missing/!object in request: %v", gc["responseSchema"])
	}
	if _, ok := sent["systemInstruction"]; !ok {
		t.Error("systemInstruction missing from request")
	}

	// Extraction: output is the candidate text; usage comes from usageMetadata.
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
	// rawBody is the verbatim provider envelope (still names the provider — that
	// is fine; it only ever lands in the internal audit).
	if !strings.Contains(string(res.rawBody), "usageMetadata") {
		t.Error("rawBody should be the verbatim provider envelope")
	}
}

func TestGeminiNon2xxNeutral(t *testing.T) {
	body := loadFixture(t, "gemini_error.json")
	srv := fixtureServer(t, http.StatusInternalServerError, body, nil)
	defer srv.Close()

	g := &geminiEngine{lbl: "bs-lm-1", mdl: "m", base: srv.URL, hc: srv.Client()}
	res, err := g.generate(context.Background(), call{parts: []string{"x"}, schema: sampleSchema})
	if err == nil {
		t.Fatal("want error on 500")
	}
	assertNeutral(t, err)
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should carry the status code: %q", err.Error())
	}
	// The provider body is preserved for the audit even though the error is neutral.
	if string(res.rawBody) != string(body) {
		t.Error("non-2xx body should be captured verbatim in rawBody for audit")
	}
}

func TestGeminiTransportErrorNeutral(t *testing.T) {
	srv := fixtureServer(t, http.StatusOK, loadFixture(t, "gemini_success.json"), nil)
	client := srv.Client()
	base := srv.URL
	srv.Close() // now unreachable

	g := &geminiEngine{lbl: "bs-lm-1", mdl: "m", base: base, hc: client}
	_, err := g.generate(context.Background(), call{parts: []string{"x"}, schema: sampleSchema})
	if err == nil {
		t.Fatal("want transport error")
	}
	assertNeutral(t, err)
	// The unreachable-endpoint URL must not leak into the error.
	assertNoLeak(t, "transport error", err.Error())
}

func TestGeminiCredentialErrorNeutral(t *testing.T) {
	srv := fixtureServer(t, http.StatusOK, loadFixture(t, "gemini_success.json"), nil)
	defer srv.Close()

	g := &geminiEngine{
		lbl: "bs-lm-1", mdl: "m", base: srv.URL, hc: srv.Client(),
		token: func(context.Context) (string, error) { return "", errors.New("adc: metadata server unreachable") },
	}
	_, err := g.generate(context.Background(), call{parts: []string{"x"}, schema: sampleSchema})
	if err == nil {
		t.Fatal("want credential error")
	}
	assertNeutral(t, err)
}

// TestGeminiInvalidOutputRetryThenFail drives the WHOLE path — Client + real
// gemini engine + a recorded fixture whose output violates the caller's struct.
// The schema-violation fixture is served for both attempts, so the Client
// retries exactly once and then hard-fails with a neutral error, recording two
// 'invalid' audit rows.
func TestGeminiInvalidOutputRetryThenFail(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(loadFixture(t, "gemini_invalid.json"))
	}))
	defer srv.Close()

	aud := &memAuditor{}
	c, err := New(Options{
		Auditor: aud,
		Logger:  discardLogger(),
		Engines: []EngineConfig{{Label: "bs-lm-1", Provider: ProviderGemini, Model: "test-model", Price: &Price{InputPerMTokCents: 1, OutputPerMTokCents: 1}}},
		Gemini:  GeminiOptions{Endpoint: srv.URL, Token: func(context.Context) (string, error) { return "t", nil }},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var out sampleOut
	_, gErr := c.Generate(context.Background(), baseRequest(&out))
	if !errors.Is(gErr, ErrInvalidOutput) {
		t.Fatalf("err = %v, want ErrInvalidOutput", gErr)
	}
	assertNeutral(t, gErr)
	if hits != 2 {
		t.Errorf("provider hits = %d, want 2 (initial + one retry)", hits)
	}
	if len(aud.rows) != 2 || aud.rows[0].Status != statusInvalid || aud.rows[1].Status != statusInvalid {
		t.Fatalf("audit rows = %+v, want two invalid rows", aud.rows)
	}
	// Each invalid attempt still stored the provider envelope verbatim.
	for i, r := range aud.rows {
		if !strings.Contains(string(r.RawResponse), "usageMetadata") {
			t.Errorf("row %d raw_response not verbatim", i)
		}
	}
}
