# Task: m1-stages-config-gate — make the active pipeline stage list config-driven; default ingest-only

**Milestone:** M1 (regression mitigation, Architect-directed 2026-07-23) · **Type:** backend + tests · **Slug:** `m1-stages-config-gate`

## Why (live regression)

The transcribe stage (committed c641226) was wired into the worker's auto-advance chain
AND deployed, but (a) the prod worker Job has NO ASR config, so in prod
(ENV=prod → ASRMode defaults to `speech`) the transcribe stage cannot build its engine and
a new upload gets stuck in `processing/transcribe`; (b) the demo/e2e upload→READY flow
times out because the auto-advance-spawned transcribe worker doesn't get the fake engine
in that environment; (c) `web/tests/token-conformance.spec.ts` and `flow.spec.ts` fail on
main. Root process gap: `make check` (the commit gate) excludes Playwright e2e, and the
loop pushes directly to main, so `pr.yml`'s e2e job never ran on these commits.

This task restores the last known-good state (ingest terminal) safely and reversibly,
WITHOUT deleting the transcribe code. Re-enabling transcribe end-to-end (prod ASR config,
demo auto-advance env fix, two-stage e2e specs) is a separate follow-up gated on a human
decision about paid prod Speech.

## Scope

1. **Config-driven stage list.** Replace the hardcoded `defaultStages` chain with an
   ordered list resolved from config: env `PIPELINE_STAGES` (comma-separated), **default
   `ingest`** (ingest terminal). The transcribe stage stays REGISTERED in the stage
   registry (its code/tests remain) but is only in the active chain when `PIPELINE_STAGES`
   includes it. Validate the list against the registry at startup (unknown stage →
   fail-fast); the list must start with `ingest`. Preserve all m1-stage-machine invariants
   (atomic claim+stamp, auto-advance loop/skip-proofing, detached SIGTERM finalize).
2. **Demo seed back to ingest-terminal.** Revert the `tools/demo/lib.sh` seed change so the
   seeded sample is seeded through `ingest` only (READY at ingest) — do NOT run transcribe
   in the seed. (The demo will exercise transcribe again in the re-enablement task, with
   the env fix.) Keep everything else in lib.sh unchanged.
3. **No e2e spec changes needed** — with ingest terminal, the seeded sample renders bar-1
   done / bars 2-5 unreached, which is exactly what `token-conformance.spec.ts` and
   `flow.spec.ts` already expect. If any e2e assertion still fails, STOP and report rather
   than editing specs (that would mean the revert is incomplete).
4. **Keep transcribe code + its unit/DB tests intact and green** (they run under make
   check independent of the chain).

## Acceptance (MANDATORY e2e — this is the whole point)

- `make check` GREEN.
- **`make e2e` GREEN** (the Architect stopped the dev server; ports 5173/8090 are free —
  run the real demo+Playwright flow). This MUST include `flow.spec.ts` (upload→READY) and
  `token-conformance.spec.ts` passing. Paste the Playwright summary in your report. If the
  demo stack needs DEMO_DATABASE_URL, it's `postgres://blueshift@localhost:5455/blueshift?sslmode=disable`.
- Confirm in the report: with `PIPELINE_STAGES` unset, the worker runs ingest-only and an
  uploaded episode reaches `ready` at `current_stage=ingest`.

## Out of scope

Prod ASR config; demo auto-advance env propagation; re-enabling transcribe in the chain;
making e2e gate the trunk (all separate follow-up tasks).

## Evidence

Summary; diffs; make check + **make e2e** transcripts; open questions.
