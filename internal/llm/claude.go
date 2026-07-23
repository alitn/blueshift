package llm

// claude.go is the second provider REST implementation behind the neutral
// Client. As with gemini.go, provider names live here by design and never escape
// to a caller: the Client maps every failure to a neutral sentinel and exposes
// only the neutral engine label.
//
// The engine calls the Messages API with output_config.format type
// "json_schema", which compiles the schema to a grammar that constrains
// generation, so the returned text is valid JSON matching the schema. The API
// key travels in the x-api-key header only — never in a URL, an error, or a log.
//
// REST shape verified 2026-07-23 against the structured-outputs docs. NOTE: the
// feature is now generally available and the request shape changed from the
// task spec's `output_format` to `output_config.format`, and the beta header is
// no longer required (the old form still works during a transition period). This
// file implements the current GA shape. The JSON output is the text of
// content[0]; token usage is in usage.{input_tokens,output_tokens}.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// claudeDefaultEndpoint is the public Messages API base used when no endpoint is
// configured.
const claudeDefaultEndpoint = "https://api.anthropic.com/v1"

// claudeDefaultMaxTokens is used when the caller sets no MaxTokens (the Messages
// API requires a positive max_tokens).
const claudeDefaultMaxTokens = 2048

// claudeDefaultVersion is the pinned Messages API version header value.
const claudeDefaultVersion = "2023-06-01"

// claudeOutputFormatType selects JSON-schema-constrained generation.
const claudeOutputFormatType = "json_schema"

// claudeEngine binds one neutral label to one concrete model on the Messages API.
type claudeEngine struct {
	lbl     string
	mdl     string
	base    string // endpoint base, e.g. "https://api.anthropic.com/v1"
	apiKey  string
	version string
	hc      *http.Client
}

var _ engine = (*claudeEngine)(nil)

func (e *claudeEngine) label() string { return e.lbl }
func (e *claudeEngine) model() string { return e.mdl }

// ----- request wire types -----

type claudeRequest struct {
	Model        string             `json:"model"`
	MaxTokens    int                `json:"max_tokens"`
	Temperature  float64            `json:"temperature,omitempty"`
	System       string             `json:"system,omitempty"`
	Messages     []claudeMessage    `json:"messages"`
	OutputConfig claudeOutputConfig `json:"output_config"`
}

type claudeMessage struct {
	Role    string        `json:"role"`
	Content []claudeBlock `json:"content"`
}

type claudeBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeOutputConfig struct {
	Format claudeFormat `json:"format"`
}

type claudeFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

// ----- response wire types -----

type claudeResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// generate issues one Messages call and extracts the structured JSON output.
// Every error is neutral; a read body is returned in result.rawBody so a failed
// attempt is still auditable. The API key is never included in any error.
func (e *claudeEngine) generate(ctx context.Context, c call) (result, error) {
	blocks := make([]claudeBlock, 0, len(c.parts))
	for _, p := range c.parts {
		blocks = append(blocks, claudeBlock{Type: "text", Text: p})
	}
	maxTok := c.maxTokens
	if maxTok <= 0 {
		maxTok = claudeDefaultMaxTokens
	}
	reqBody := claudeRequest{
		Model:       e.mdl,
		MaxTokens:   maxTok,
		Temperature: c.temperature,
		System:      c.system,
		Messages:    []claudeMessage{{Role: "user", Content: blocks}},
		OutputConfig: claudeOutputConfig{
			Format: claudeFormat{Type: claudeOutputFormatType, Schema: c.schema},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return result{}, fmt.Errorf("llm: marshal request: %w", err)
	}

	endpoint := e.base + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return result{}, errors.New("llm: build request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", e.version)
	if e.apiKey != "" {
		req.Header.Set("x-api-key", e.apiKey)
	}

	resp, err := e.hc.Do(req)
	if err != nil {
		// The key travels in a header, not the URL, so the transport error's
		// embedded URL cannot leak it; still, keep only the underlying cause.
		return result{}, fmt.Errorf("llm: engine request: %w", transportCause(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody))

	if resp.StatusCode != http.StatusOK {
		return result{rawBody: raw}, fmt.Errorf("llm: engine status %d", resp.StatusCode)
	}

	var parsed claudeResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return result{rawBody: raw}, fmt.Errorf("llm: decode response envelope: %w", err)
	}
	output := firstTextBlock(parsed)
	if output == "" {
		return result{rawBody: raw}, errors.New("llm: response has no output")
	}

	return result{
		rawBody: raw,
		output:  []byte(output),
		usage: usage{
			inputTokens:  parsed.Usage.InputTokens,
			outputTokens: parsed.Usage.OutputTokens,
		},
	}, nil
}

// firstTextBlock returns the text of the first text content block (where the
// schema-constrained JSON output lives), or "" if there is none.
func firstTextBlock(r claudeResponse) string {
	for _, b := range r.Content {
		if b.Type == "text" && b.Text != "" {
			return b.Text
		}
	}
	return ""
}
