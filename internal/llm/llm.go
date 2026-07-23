// Package llm is the single seam through which the app asks a language model for
// a schema-constrained answer. Everything downstream (diarization merges,
// moment selection, and later copy tasks) calls Client.Generate with a neutral
// engine label, a prompt, and a JSON schema; it never talks to a provider
// directly. Three properties are enforced here, in one place:
//
//   - Vendor neutrality. Callers select an engine by a neutral label ("bs-lm-1")
//     that config binds to a concrete provider + model at runtime. Provider and
//     model names live ONLY in this package (the vendor-leak gate does not scan
//     it) and in config/deploy; they never appear in the Response, in a returned
//     error, or in any client-visible surface. Errors returned to callers are the
//     neutral sentinels below, optionally carrying an opaque internal error id.
//
//   - Schema-validated output. The JSON schema is sent to the provider so the
//     engine enforces it server-side, and the returned bytes are then strict-
//     decoded locally into the caller's Go struct (unknown fields rejected) plus
//     an optional semantic hook. An output that fails either check is retried
//     exactly once (a fresh call); a second failure is a hard, neutral error.
//
//   - Audited. Every provider call — success, schema-invalid output, or a failed
//     attempt — is written to llm_calls through the Auditor seam, so the retry
//     and its cost are both on the record.
//
// The registry mirrors /internal/lang: a small map from a neutral label to a
// fully-configured engine, resolved at runtime from data, never hardcoded. The
// boundary error model mirrors /internal/auth and /internal/blob: raw provider
// causes are wrapped for server-side logs only and classified into neutral
// sentinels before they cross this package.
package llm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Sentinel errors. Everything that can go wrong collapses into one of these
// before leaving the package, so callers never branch on — or render — a
// provider-specific cause. None of these strings names a provider.
var (
	// ErrBadRequest means the Request was malformed (missing engine, schema,
	// output target, or org scope). It is a programming error at the call site,
	// not a model failure: no provider call is made and nothing is audited.
	ErrBadRequest = errors.New("llm: invalid request")
	// ErrUnknownEngine means the request's engine label is not registered.
	ErrUnknownEngine = errors.New("llm: unknown engine")
	// ErrInvalidOutput means the model's output failed strict local validation
	// on both the initial call and its one retry.
	ErrInvalidOutput = errors.New("llm: model output failed validation")
	// ErrUnavailable means the engine could not be reached or answer (transport
	// failure, non-2xx, or an unparseable response) on both attempts. The raw
	// cause is logged server-side only.
	ErrUnavailable = errors.New("llm: engine unavailable")
)

// Call outcome statuses recorded on each llm_calls row (see migration 0004).
const (
	statusOK      = "ok"
	statusInvalid = "invalid"
	statusError   = "error"
)

// maxAttempts is one initial call plus exactly one retry (CLAUDE.md: invalid →
// one retry → hard fail).
const maxAttempts = 2

// Request is one schema-constrained generation. Engine is a neutral label the
// registry binds to a concrete model; Schema is sent to the provider for
// server-side enforcement AND is the contract the output is strict-decoded
// against locally. OrgID (and optionally EpisodeID) scope the audit row.
type Request struct {
	// Engine is the neutral engine label to run (e.g. "bs-lm-1").
	Engine string
	// PromptID and PromptVersion identify the prompt template for auditing.
	// PromptVersion is stored on every llm_calls row.
	PromptID      string
	PromptVersion string
	// System is an optional system instruction.
	System string
	// Parts are the ordered user input parts (concatenated into one user turn).
	Parts []string
	// Schema is the raw JSON schema (a provider-agnostic subset) constraining the
	// output. It is required: server-side enforcement is the whole point.
	Schema json.RawMessage
	// Temperature and MaxTokens are generation controls. MaxTokens <= 0 lets the
	// engine apply its default.
	Temperature float64
	MaxTokens   int
	// OrgID scopes the audit row to a tenant (required). EpisodeID is optional
	// (0 => not tied to a specific episode).
	OrgID     int64
	EpisodeID int64
	// Out is a non-nil pointer to the caller's target struct. The validated JSON
	// output is strict-decoded (unknown fields rejected) into it.
	Out any
	// Validate is an optional semantic hook run after a successful decode; a
	// non-nil error from it is treated exactly like a decode failure (retry once,
	// then hard fail). It typically inspects Out.
	Validate func() error
}

// Response is a successful generation. Raw is the model's structured JSON output
// (also already decoded into Request.Out). CostCents is nil when no price is
// configured for the engine's model. Engine echoes the neutral label; no
// concrete model or provider name is ever exposed here.
type Response struct {
	Engine    string
	Raw       json.RawMessage
	CostCents *int
	LatencyMS int
	Attempts  int
}

// Price is an engine's per-token rate, expressed as integer cents per one
// million tokens (money stays in integer cents per repo conventions). A nil
// *Price on a registered engine means the price is unknown: cost is recorded as
// NULL and a WARN is logged.
type Price struct {
	InputPerMTokCents  int
	OutputPerMTokCents int
}

// cents converts token usage to whole integer cents, rounded to nearest.
func (p Price) cents(u usage) int {
	num := int64(u.inputTokens)*int64(p.InputPerMTokCents) +
		int64(u.outputTokens)*int64(p.OutputPerMTokCents)
	return int((num + 500_000) / 1_000_000)
}

// usage is the neutral token accounting each engine extracts from its provider
// response and hands back for costing.
type usage struct {
	inputTokens  int
	outputTokens int
}

// call is the provider-agnostic instruction one engine issues to its provider.
type call struct {
	system      string
	parts       []string
	schema      json.RawMessage
	temperature float64
	maxTokens   int
}

// result is what an engine returns from one provider call. rawBody is the
// verbatim provider response body (stored in the audit as jsonb when it is valid
// JSON); output is the extracted structured JSON to strict-decode into the
// caller's struct.
type result struct {
	rawBody []byte
	output  []byte
	usage   usage
}

// engine is one fully-configured, provider-backed model binding. Implementations
// (geminiEngine, claudeEngine) live in this package — the only place provider
// names may appear. generate issues exactly one provider call; retries and
// auditing are the Client's job, above this seam.
type engine interface {
	// label is the neutral engine label this binding answers to.
	label() string
	// model is the concrete model id, used only for the internal audit row.
	model() string
	// generate issues one provider call. On any failure it returns a neutral
	// error (raw cause suitable for server logs, never a provider name) and, when
	// a response body was read, a result whose rawBody is set so the failed
	// attempt can still be audited.
	generate(ctx context.Context, c call) (result, error)
}

// Auditor persists one llm_calls row per provider call. It is the seam that
// keeps this package free of database types; the store implements it (see
// dbaudit.go for the production adapter).
type Auditor interface {
	RecordLLMCall(ctx context.Context, rec CallRecord) error
}

// CallRecord is one audit row's worth of data, in neutral (no database types)
// form. RawResponse is stored verbatim; a nil RawResponse or CostCents is
// recorded as SQL NULL.
type CallRecord struct {
	OrgID         int64
	EpisodeID     int64
	Model         string
	PromptVersion string
	InputHash     string
	RawResponse   []byte
	CostCents     *int
	LatencyMS     int
	Status        string
}

// registered is one label's binding in the registry: its engine plus the
// (optional) price used to cost its calls.
type registered struct {
	eng   engine
	price *Price
}

// Client resolves a neutral engine label to its engine, runs the validate-and-
// retry loop, and audits every attempt. It is safe for concurrent use.
type Client struct {
	reg   map[string]registered
	audit Auditor
	log   *slog.Logger
	now   func() time.Time
}

// Generate runs req against its engine: one call, strict-decode + optional
// semantic validation of the output into req.Out, exactly one retry on failure,
// then a neutral hard error. Every attempt is audited. On success the decoded
// value is in req.Out and also returned raw in Response.Raw.
func (c *Client) Generate(ctx context.Context, req Request) (Response, error) {
	if err := req.validate(); err != nil {
		return Response{}, err
	}
	reg, ok := c.reg[req.Engine]
	if !ok {
		return Response{}, fmt.Errorf("%w: %q", ErrUnknownEngine, req.Engine)
	}

	inputHash, err := hashInput(reg.eng.model(), req)
	if err != nil {
		return Response{}, fmt.Errorf("%w: canonicalize input: %v", ErrBadRequest, err)
	}

	instr := call{
		system:      req.System,
		parts:       req.Parts,
		schema:      req.Schema,
		temperature: req.Temperature,
		maxTokens:   req.MaxTokens,
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// A cancelled context is not an engine failure: stop without another
		// attempt and surface the cancellation (nothing to audit — no call went
		// out).
		if err := ctx.Err(); err != nil {
			return Response{}, err
		}

		start := c.now()
		res, genErr := reg.eng.generate(ctx, instr)
		latency := int(c.now().Sub(start) / time.Millisecond)

		if genErr != nil {
			// Upstream call failed: record the attempt (with whatever body came
			// back, if any) and retry.
			c.record(ctx, req, reg, inputHash, res.rawBody, nil, latency, statusError)
			c.log.LogAttrs(ctx, slog.LevelWarn, "llm call attempt failed",
				slog.String("engine", req.Engine),
				slog.Int("attempt", attempt),
				slog.String("cause", genErr.Error()))
			lastErr = ErrUnavailable
			continue
		}

		// The call returned a body; it is billable regardless of whether the
		// output validates, so cost is computed now.
		cost := c.cost(ctx, req.Engine, reg, res.usage)

		if decErr := decodeStrict(res.output, req.Out); decErr != nil {
			c.record(ctx, req, reg, inputHash, res.rawBody, cost, latency, statusInvalid)
			c.log.LogAttrs(ctx, slog.LevelWarn, "llm output failed strict decode",
				slog.String("engine", req.Engine),
				slog.Int("attempt", attempt),
				slog.String("cause", decErr.Error()))
			lastErr = ErrInvalidOutput
			continue
		}
		if req.Validate != nil {
			if vErr := req.Validate(); vErr != nil {
				c.record(ctx, req, reg, inputHash, res.rawBody, cost, latency, statusInvalid)
				c.log.LogAttrs(ctx, slog.LevelWarn, "llm output failed semantic validation",
					slog.String("engine", req.Engine),
					slog.Int("attempt", attempt),
					slog.String("cause", vErr.Error()))
				lastErr = ErrInvalidOutput
				continue
			}
		}

		c.record(ctx, req, reg, inputHash, res.rawBody, cost, latency, statusOK)
		return Response{
			Engine:    req.Engine,
			Raw:       append(json.RawMessage(nil), res.output...),
			CostCents: cost,
			LatencyMS: latency,
			Attempts:  attempt,
		}, nil
	}

	// Both attempts failed. Return the neutral sentinel with an opaque error id
	// that ties the caller's failure to the server-side WARN lines above.
	id := errorID()
	c.log.LogAttrs(ctx, slog.LevelError, "llm generate exhausted retries",
		slog.String("engine", req.Engine),
		slog.String("error_id", id))
	return Response{}, fmt.Errorf("%w [%s]", lastErr, id)
}

// cost computes the call's cents from configured price and usage. An engine with
// no configured price yields a nil cost and a single WARN (the call still bills
// upstream; we simply cannot value it here).
func (c *Client) cost(ctx context.Context, label string, reg registered, u usage) *int {
	if reg.price == nil {
		c.log.LogAttrs(ctx, slog.LevelWarn, "llm call cost unknown: no price configured",
			slog.String("engine", label))
		return nil
	}
	cents := reg.price.cents(u)
	return &cents
}

// record writes one audit row, best-effort: a failed audit write is logged at
// ERROR (with enough to reconstruct the row) but never turns a completed
// provider call into a caller-visible failure. rawBody is stored only when it is
// valid JSON (the jsonb column cannot hold anything else); otherwise NULL.
func (c *Client) record(ctx context.Context, req Request, reg registered, inputHash string, rawBody []byte, cost *int, latency int, status string) {
	var raw []byte
	if json.Valid(rawBody) {
		raw = rawBody
	}
	rec := CallRecord{
		OrgID:         req.OrgID,
		EpisodeID:     req.EpisodeID,
		Model:         reg.eng.model(),
		PromptVersion: req.PromptVersion,
		InputHash:     inputHash,
		RawResponse:   raw,
		CostCents:     cost,
		LatencyMS:     latency,
		Status:        status,
	}
	if err := c.audit.RecordLLMCall(ctx, rec); err != nil {
		c.log.LogAttrs(ctx, slog.LevelError, "llm audit write failed",
			slog.String("engine", req.Engine),
			slog.String("status", status),
			slog.String("input_hash", inputHash),
			slog.String("cause", err.Error()))
	}
}

// validate rejects a malformed request before any provider call is made.
func (r Request) validate() error {
	switch {
	case r.Engine == "":
		return fmt.Errorf("%w: engine is required", ErrBadRequest)
	case r.OrgID <= 0:
		return fmt.Errorf("%w: org id is required", ErrBadRequest)
	case len(r.Schema) == 0:
		return fmt.Errorf("%w: schema is required", ErrBadRequest)
	case r.Out == nil:
		return fmt.Errorf("%w: output target is required", ErrBadRequest)
	default:
		return nil
	}
}

// decodeStrict decodes output into out rejecting unknown fields and trailing
// data, so an output the provider "matched" server-side is still checked against
// the caller's exact Go struct (a schema superset, extra keys, or garbage tail
// all fail here and trigger the retry).
func decodeStrict(output []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(output))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected trailing data after JSON value")
	}
	return nil
}

// hashInput returns the sha256 (hex) of a canonical serialization of everything
// that defines the call's input: the concrete model, prompt id + version, system
// text, ordered parts, the schema (re-serialized so whitespace/key-order does
// not change the hash), and the generation controls. It is stable across
// attempts and across cosmetically different but semantically identical inputs.
func hashInput(model string, req Request) (string, error) {
	schema, err := canonicalJSON(req.Schema)
	if err != nil {
		return "", err
	}
	canonical := struct {
		Model         string          `json:"model"`
		PromptID      string          `json:"prompt_id"`
		PromptVersion string          `json:"prompt_version"`
		System        string          `json:"system"`
		Parts         []string        `json:"parts"`
		Schema        json.RawMessage `json:"schema"`
		Temperature   float64         `json:"temperature"`
		MaxTokens     int             `json:"max_tokens"`
	}{
		Model:         model,
		PromptID:      req.PromptID,
		PromptVersion: req.PromptVersion,
		System:        req.System,
		Parts:         req.Parts,
		Schema:        schema,
		Temperature:   req.Temperature,
		MaxTokens:     req.MaxTokens,
	}
	b, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalJSON re-serializes raw so semantically equal JSON produces identical
// bytes (object keys sorted by encoding/json, insignificant whitespace dropped).
// Empty input canonicalizes to a JSON null.
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("null"), nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// errorID returns a short opaque hex id correlating a caller-visible failure
// with the server log line that holds the raw cause. It names nothing.
func errorID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}
