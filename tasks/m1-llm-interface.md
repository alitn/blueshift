# Task: m1-llm-interface — /internal/llm: schema-validated calls + audit

**Milestone:** M1 · **Type:** backend · **Slug:** `m1-llm-interface`

## Researched ruling (sources cited — standing rule)

Both target engines enforce JSON schema **server-side**, so no client-side JSON-Schema
validator dependency is needed (zero new deps → no ADR):
- Gemini REST `generateContent`: `generationConfig.responseMimeType: "application/json"`
  + `responseSchema` (OpenAPI-subset schema; output guaranteed to parse and match).
  Source: ai.google.dev/gemini-api/docs/structured-output.
- Claude REST Messages API: `output_format`/`output_config` type `json_schema` —
  schema compiled to a grammar constraining token generation.
  Source: platform.claude.com/docs/en/build-with-claude/structured-outputs.

Local validation = strict decode into the caller's Go struct (`json.Decoder` with
`DisallowUnknownFields`) + caller-provided semantic check hook. Invalid → one retry →
hard fail (CLAUDE.md). Both engines called via REST with existing auth deps only
(Gemini through the GCP endpoint using the oauth2/ADC plumbing already in the module;
Claude via API key from Secret Manager) — follows the identity-platform REST precedent.

## Scope

1. **Interface:** `Client.Generate(ctx, Request) (Response, error)` where Request carries:
   engine label (neutral, e.g. `bs-lm-1`/`bs-lm-2` mapped to concrete models in config),
   prompt id + version, input parts, target schema (Go struct + raw schema JSON),
   temperature/max tokens. Engine registry maps neutral labels → provider impls; selection
   via config rows / env, resolved at runtime (registry pattern mirrors /internal/lang).
2. **Two impls:** `gemini` and `claude` REST clients inside /internal/llm (provider names
   allowed only there). Timeouts, context propagation, neutral error mapping with internal
   error IDs at the boundary (mirror /internal/blob + /internal/auth patterns). No
   provider strings in returned errors.
3. **Validation loop:** schema sent server-side; response strict-decoded; on parse/decode
   failure → exactly one retry (fresh call, note in audit) → hard fail neutral error.
4. **Audit:** every call (including failed/retried) inserted into existing `llm_calls`
   (model, prompt_version, input_hash sha256, raw_response jsonb, cost integer cents,
   latency, status). Cost computed from a config-row price table (per-token rates as
   config, NOT code constants); unknown price → cost NULL, WARN log.
5. **Tests:** record/replay HTTP fixtures (repo convention); schema-violation fixture →
   retry → fail path; audit row assertions (DB-backed); vendor-leak: assert error strings
   and DTO-visible surfaces carry no provider names. NO live calls in CI.

## Out of scope

Prompts for real stages (diarize/moments — later tasks); embeddings (transcribe task);
streaming; batch.

## Acceptance

- make check green; DB-backed audit tests run; record/replay only.
- Reviewer verifies: provider names confined to /internal/llm + config/deploy; one-retry
  semantics exact; input_hash stable; raw_response stored verbatim; API keys never logged.

## Evidence

Summary; diffs; test transcript; fixture list; open questions.
