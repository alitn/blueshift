# Task: m1-cost-safety — billable calls structurally bounded (prereq for real Chirp/LLM)

**Milestone:** M1 (human-directed 2026-07-23) · **Type:** backend + infra · **Slug:** `m1-cost-safety`
**Blocks:** any real Chirp/LLM engine going live in the pipeline (transcript slice, diarize live).

## Why

The human requires that no billable service (Chirp ASR, LLM) can rack up large cost via a
bug or loop. This must be in place BEFORE real engines run in the pipeline. See the
"Billable-service cost safety" standing rule in CLAUDE.md.

## Scope

1. **Idempotent billable stages (code):** before calling the billable engine, a stage
   checks whether its output already exists and SKIPS the call if so:
   - transcribe: if the episode already has segments → skip ASR (re-transcribe only on an
     explicit reprocess signal, not on a plain retry/re-drive).
   - diarize: if segments already have speaker_keys → skip the LLM call.
   Persist enough state to make "already done" unambiguous. Add tests proving a second run
   makes ZERO billable calls (assert via the fake engine's call counter / llm_calls delta).
2. **Per-episode attempt cap (code + additive migration):** `episodes.process_attempts int
   NOT NULL DEFAULT 0` (or per-stage); increment on each billable stage start; above a cap
   (env `MAX_PROCESS_ATTEMPTS`, default e.g. 5) the stage hard-fails WITHOUT calling the
   engine and logs a neutral error. Prevents an unforeseen re-drive loop from billing.
3. **Bounded retries audit:** confirm + test that /internal/asr and /internal/llm never
   retry more than the documented once/twice; no code path calls a billable engine in a
   loop without a bound. Document the max calls per episode per stage.
4. **Kill switch doc:** document that `PIPELINE_STAGES=ingest` instantly stops all Chirp/LLM
   calls with no deploy (already true via m1-stages-config-gate) — the operator escape hatch.
5. **GCP backstops (Architect operational, tracked here):** a billing budget + alert on the
   project; Cloud Speech + (if applicable) LLM API quota caps sized to PoC. Record the exact
   commands/values in deploy/README.md. These bound cost even if code guards fail.

## Acceptance

- make check green; tests prove: re-running a completed stage bills nothing; the attempt
  cap blocks a runaway before any billable call; retries are bounded.
- Reviewer verifies idempotency guards wrap EVERY billable call site; no unbounded loop
  reaches a billable engine; the migration is additive.
- Architect confirms the GCP budget alert + quota caps are live (values in deploy/README).

## Evidence

Summary; diffs; the "second run bills zero" test transcript; the GCP backstop values; open questions.
