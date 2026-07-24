# Task: m1-llm-token-budget — thinking tokens eat maxOutputTokens (fixes 249-segment diarize, pre-empts moments)

**Milestone:** M1 (prod failure, root-caused with live receipts) · **Type:** backend small · **Slug:** `m1-llm-token-budget`

## Receipts (Architect live probes, 2026-07-24 ~22:40 UTC, wire-identical replays of the prod diarize request for the 249-segment episode)

| probe | finishReason | prompt | thoughts | answer | outcome |
|---|---|---|---|---|---|
| 1 | STOP | 13,235 | 5,590 | 1,103 | VALID 27-turn tiling, S1–S3 |
| 2 | MAX_TOKENS | 13,235 | 7,865 | 301 | JSON truncated mid-array |
| 3 | MAX_TOKENS | 13,235 | 7,861 | 316 | JSON truncated mid-array |

Root cause: on the Gemini API, **model thinking tokens count against
`maxOutputTokens`**. Thinking scales with input size: ~5.6–7.9k tokens at 249
segments (fits easily at 17 segments — why the clip always passed). With the cap at
8192 the answer gets truncated on most runs → strict decode fails → the neutral
"model output failed validation" → retry → same → stage fails. The range contract
(04a97c3) is correct and NOT the problem — probe 1 proves the model solves the task
in ~1.1k answer tokens. Prod is 6/6 failed calls across two runs; probes are 1/3
lucky. `Temperature: 0` is dropped by `omitempty` (float64) so prod runs at API
default — a separate observation, noted below.

## Fix

1. **Raise the caps.** `internal/diarize/diarize.go` and `internal/moments/moments.go`
   + `compose.go`: `maxOutputTokens 8192 → 32768` (or the model's documented output
   ceiling if lower — research and cite). Rewrite the constant's comment to state the
   provider-verified fact: thinking tokens count against this cap and scale with
   input, so the cap must budget thinking + answer, not answer alone. Worst-case cost
   per call at 32768 out-tokens ≈ 30¢ at current prod pricing; typical observed ≈ 9k
   total out-tokens ≈ 8¢ — bounded by the existing per-call retry cap and per-episode
   attempt cap either way (no cost-safety change).
2. **Truncation is its own error.** `internal/llm`: when the provider reports
   truncation (Gemini `finishReason: "MAX_TOKENS"`; Claude `stop_reason:
   "max_tokens"`), fail that attempt with a distinct internal error ("output
   truncated at N tokens", neutral outward like everything else) instead of letting
   truncated JSON fall through to decode/validation failure. Retry semantics
   unchanged (one bounded retry — a truncated attempt is still an attempt). This
   exists so the NEXT budget failure is diagnosable from one log line; today's cost a
   full loop cycle to distinguish from a contract failure.
3. **Thinking control — research, then bound if supported.** Per research-first: check
   the current Vertex generateContent docs for an explicit thinking control on this
   model generation (`generationConfig.thinkingConfig` — budget or level). If
   supported for the prod model, set a bounded value chosen with cited rationale
   (probes show ~6–8k thinking is what the task actually uses; do NOT starve it below
   observed need). If not supported/deprecated for this generation, document that in
   the constant comment and rely on the raised cap. No other generation knobs change.
4. **Temperature observation (fix if trivial, else document):** `Temperature: 0` never
   reaches the wire (`omitempty` on float64 drops zero). Either make the client
   distinguish unset-vs-zero honestly (pointer or explicit-send flag) or change the
   diarize/moments requests to stop claiming 0 — pick the smaller diff that makes the
   code stop lying; note the choice. No behavior change is required beyond honesty
   (all prod traffic so far ran at API default).

## Tests

- llm client: recorded fixture for a MAX_TOKENS response → distinct truncation error,
  one retry, audit rows record the truncated attempt; existing validation-failure
  path untouched.
- diarize/moments: caps asserted (a test pinning the request's MaxTokens ≥ 32768 or
  documented ceiling).
- make check + make eval green; goldens unchanged (fake fixtures are STOP-shaped).

## Acceptance

- Reviewer verifies: cost-safety untouched (same call sites/skips/caps), truncation
  error neutral outward, thinking-control claim matches cited docs, no fixture
  regeneration beyond the added truncation case.
- Architect post-deploy: FINAL retry of the failed 44-min episode
  (process_attempts 7/10 — exactly one full stage cycle left) → diarize passes at
  249 segments → moments → episode READY. The probes predict success: answer needs
  ~1.1k tokens; the new budget leaves ≥20k headroom over worst observed thinking.

## Evidence

Summary; diffs; truncation-fixture transcript; cited docs for thinking control and
output ceiling; open questions.
