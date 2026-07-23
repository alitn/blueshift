package llm

// gemini.go is one of the two provider REST implementations behind the neutral
// Client. Provider names appear here by design: this package is the boundary the
// vendor-leak gate deliberately does not scan, and nothing in this file's
// request/response types or errors escapes to a caller (the Client collapses
// every failure into a neutral sentinel and never exposes the concrete model).
//
// The engine calls generateContent with generationConfig.responseMimeType
// "application/json" + responseSchema, so the schema is enforced server-side and
// the response is guaranteed to be JSON matching it. Auth is a bearer token from
// the module's oauth2/ADC plumbing (see adc.go); tests inject a static token via
// the tokenFn seam so no credential is ever needed offline.
//
// REST shape verified 2026-07-23 against ai.google.dev/api/generate-content:
// generationConfig.{responseMimeType,responseSchema}; the JSON output is the
// text of candidates[0].content.parts[0]; token usage is in usageMetadata.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// geminiResponseMimeType is the value that puts the engine in JSON mode; paired
// with responseSchema it makes the output parse and match server-side.
const geminiResponseMimeType = "application/json"

// geminiDefaultMaxTokens caps output when the caller sets no MaxTokens.
const geminiDefaultMaxTokens = 2048

// geminiCloudScope is the OAuth scope an ADC token must carry to call the
// platform's prediction endpoint.
const geminiCloudScope = "https://www.googleapis.com/auth/cloud-platform"

// tokenFn returns a bearer access token for the request. It is the seam over the
// oauth2/ADC plumbing so the transport can be exercised offline with a static
// token. A nil tokenFn means "send no Authorization header" (test servers).
type tokenFn func(ctx context.Context) (string, error)

// geminiEngine binds one neutral label to one concrete model on the platform's
// generateContent REST endpoint.
type geminiEngine struct {
	lbl   string
	mdl   string
	base  string // endpoint up to ".../models", no trailing slash
	token tokenFn
	hc    *http.Client
}

var _ engine = (*geminiEngine)(nil)

func (g *geminiEngine) label() string { return g.lbl }
func (g *geminiEngine) model() string { return g.mdl }

// ----- request wire types -----

type geminiRequest struct {
	Contents          []geminiContent   `json:"contents"`
	SystemInstruction *geminiContent    `json:"systemInstruction,omitempty"`
	GenerationConfig  geminiGenerConfig `json:"generationConfig"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenerConfig struct {
	ResponseMimeType string          `json:"responseMimeType"`
	ResponseSchema   json.RawMessage `json:"responseSchema"`
	Temperature      float64         `json:"temperature,omitempty"`
	MaxOutputTokens  int             `json:"maxOutputTokens,omitempty"`
}

// ----- response wire types -----

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

// generate issues one generateContent call and extracts the structured JSON
// output. Every error is neutral (no provider name); a body that was read is
// returned in result.rawBody so a failed attempt is still auditable.
func (g *geminiEngine) generate(ctx context.Context, c call) (result, error) {
	parts := make([]geminiPart, 0, len(c.parts))
	for _, p := range c.parts {
		parts = append(parts, geminiPart{Text: p})
	}
	maxTok := c.maxTokens
	if maxTok <= 0 {
		maxTok = geminiDefaultMaxTokens
	}
	reqBody := geminiRequest{
		Contents: []geminiContent{{Role: "user", Parts: parts}},
		GenerationConfig: geminiGenerConfig{
			ResponseMimeType: geminiResponseMimeType,
			ResponseSchema:   c.schema,
			Temperature:      c.temperature,
			MaxOutputTokens:  maxTok,
		},
	}
	if c.system != "" {
		reqBody.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: c.system}}}
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return result{}, fmt.Errorf("llm: marshal request: %w", err)
	}

	endpoint := g.base + "/" + g.mdl + ":generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		// A NewRequest error can embed the endpoint URL; drop it for a neutral cause.
		return result{}, errors.New("llm: build request")
	}
	req.Header.Set("Content-Type", "application/json")

	if g.token != nil {
		tok, terr := g.token(ctx)
		if terr != nil {
			return result{}, fmt.Errorf("llm: acquire credentials: %w", terr)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := g.hc.Do(req)
	if err != nil {
		// A transport error embeds the request URL; keep only the cause.
		return result{}, fmt.Errorf("llm: engine request: %w", transportCause(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody))

	if resp.StatusCode != http.StatusOK {
		// Non-2xx: return the body for the audit but a neutral, name-free error.
		return result{rawBody: raw}, fmt.Errorf("llm: engine status %d", resp.StatusCode)
	}

	var parsed geminiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return result{rawBody: raw}, fmt.Errorf("llm: decode response envelope: %w", err)
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return result{rawBody: raw}, errors.New("llm: response has no output")
	}
	output := parsed.Candidates[0].Content.Parts[0].Text
	if output == "" {
		return result{rawBody: raw}, errors.New("llm: response output is empty")
	}

	return result{
		rawBody: raw,
		output:  []byte(output),
		usage: usage{
			inputTokens:  parsed.UsageMetadata.PromptTokenCount,
			outputTokens: parsed.UsageMetadata.CandidatesTokenCount,
		},
	}, nil
}

// buildGeminiBase returns the generateContent endpoint prefix (up to
// ".../models") from a project + region when no explicit endpoint is configured.
func buildGeminiBase(region, project string) string {
	return fmt.Sprintf(
		"https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models",
		region, project, region,
	)
}

// maxProviderBody bounds how much of a provider response we read.
const maxProviderBody = 1 << 20

// httpTimeout bounds a single provider call.
const httpTimeout = 60 * time.Second

// transportCause unwraps a *url.Error to its underlying cause, dropping the
// request URL from the message (it can carry query credentials or endpoints).
func transportCause(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}
