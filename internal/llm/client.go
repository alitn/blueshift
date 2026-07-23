package llm

// client.go builds the Client's engine registry from runtime configuration. The
// mapping from a neutral label to a concrete provider + model + price is data
// (env / config rows resolved by the caller), never code constants — the same
// stance /internal/lang takes on engine selection. Provider names appear only in
// this package and in that config; they never reach a client-visible surface.

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Provider identifiers a config row binds a neutral label to. These are internal
// selectors, never exposed to callers.
const (
	ProviderGemini = "gemini"
	ProviderClaude = "claude"
)

// EngineConfig declares one neutral label's binding: which provider backs it, the
// concrete model to call, and its price (nil => unknown, cost recorded NULL).
// This is the per-engine data the registry is built from at runtime.
type EngineConfig struct {
	Label    string
	Provider string
	Model    string
	Price    *Price
}

// GeminiOptions carries the shared wiring for every gemini-backed engine. When
// Endpoint is empty it is derived from Project + Region. Token is the bearer
// source; nil selects Application Default Credentials (adc.go).
type GeminiOptions struct {
	Endpoint string
	Project  string
	Region   string
	Token    tokenFn
}

// ClaudeOptions carries the shared wiring for every claude-backed engine.
// Endpoint defaults to the public Messages API base; Version defaults to the
// pinned API version. APIKey is injected from Secret Manager via env and is
// never logged.
type ClaudeOptions struct {
	Endpoint string
	APIKey   string
	Version  string
}

// Options fully specifies a Client: the engines to register, the audit sink, and
// the per-provider wiring. Logger, HTTPClient, and Now default when unset.
type Options struct {
	Engines    []EngineConfig
	Auditor    Auditor
	Logger     *slog.Logger
	HTTPClient *http.Client
	Gemini     GeminiOptions
	Claude     ClaudeOptions
	Now        func() time.Time
}

// New builds a Client from opts, instantiating one engine per config row. It
// fails fast on a missing auditor, an empty or duplicate label, an unknown
// provider, or missing provider wiring — all startup misconfigurations that must
// not resolve ambiguously later.
func New(opts Options) (*Client, error) {
	if opts.Auditor == nil {
		return nil, errors.New("llm: auditor is required")
	}
	if len(opts.Engines) == 0 {
		return nil, errors.New("llm: at least one engine must be configured")
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: httpTimeout}
	}

	reg := make(map[string]registered, len(opts.Engines))
	for _, ec := range opts.Engines {
		if ec.Label == "" {
			return nil, errors.New("llm: engine label is required")
		}
		if _, dup := reg[ec.Label]; dup {
			return nil, fmt.Errorf("llm: duplicate engine label %q", ec.Label)
		}
		if ec.Model == "" {
			return nil, fmt.Errorf("llm: engine %q: model is required", ec.Label)
		}
		eng, err := buildEngine(ec, opts, hc)
		if err != nil {
			return nil, err
		}
		reg[ec.Label] = registered{eng: eng, price: ec.Price}
	}

	return &Client{reg: reg, audit: opts.Auditor, log: logger, now: now}, nil
}

// buildEngine instantiates the concrete engine for one config row.
func buildEngine(ec EngineConfig, opts Options, hc *http.Client) (engine, error) {
	switch ec.Provider {
	case ProviderGemini:
		base := opts.Gemini.Endpoint
		if base == "" {
			if opts.Gemini.Project == "" || opts.Gemini.Region == "" {
				return nil, fmt.Errorf("llm: engine %q: gemini endpoint, or project and region, is required", ec.Label)
			}
			base = buildGeminiBase(opts.Gemini.Region, opts.Gemini.Project)
		}
		tok := opts.Gemini.Token
		if tok == nil {
			tok = adcTokenFunc(geminiCloudScope)
		}
		return &geminiEngine{
			lbl:   ec.Label,
			mdl:   ec.Model,
			base:  strings.TrimRight(base, "/"),
			token: tok,
			hc:    hc,
		}, nil
	case ProviderClaude:
		base := opts.Claude.Endpoint
		if base == "" {
			base = claudeDefaultEndpoint
		}
		version := opts.Claude.Version
		if version == "" {
			version = claudeDefaultVersion
		}
		return &claudeEngine{
			lbl:     ec.Label,
			mdl:     ec.Model,
			base:    strings.TrimRight(base, "/"),
			apiKey:  opts.Claude.APIKey,
			version: version,
			hc:      hc,
		}, nil
	default:
		return nil, fmt.Errorf("llm: engine %q: unknown provider %q", ec.Label, ec.Provider)
	}
}
