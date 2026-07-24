# Task: m1-engine-brand-label — spell out "Blueshift" in UI engine labels (human-directed 2026-07-24)

**Milestone:** M1 · **Type:** tiny UI (fast-loop eligible) · **Slug:** `m1-engine-brand-label`

## Directive

The human: "use Blueshift instead of BS on ui". The pipeline hover card currently
renders public engine labels as `BS·ASR 2` / `BS·LM 1` / `BS·MEDIA 1`
(`web/src/lib/pipelineDetails.ts` `engineDisplay`).

## Scope

1. `engineDisplay`: map the leading `bs` token to the product name — `bs-asr-2` →
   `BLUESHIFT·ASR 2`, `bs-lm-1` → `BLUESHIFT·LM 1`, `bs-media-1` → `BLUESHIFT·MEDIA 1`.
   Only the FIRST token when it equals `bs` expands; all other tokens keep the existing
   uppercase behavior; unknown shapes unchanged (uppercased as today). API labels/DTOs
   are untouched — this is display-only.
2. Update the unit tests (`pipelineDetails.test.ts`) and the two e2e text assertions
   (`web/tests/pipeline-details.spec.ts:58-59`).
3. Verify the popover layout still fits the longer word at both viewports (the card is
   the widest element consumer) — screenshot evidence to `.artifacts/screens/
   m1-engine-brand-label/`; no committed visual baselines cover the popover, so zero
   baseline drift is expected.

## Out of scope

Any change to label values in API/DTO/DB/deploy env; any other UI string.

## Acceptance

Tiny-diff tier (CLAUDE.md fast UI loop): targeted checks (vitest pipelineDetails,
svelte-check, eslint touched files, the pipeline-details e2e) + evidence; the commit
gate runs the full suite deterministically. Reviewer verifies display-only scope and
the popover evidence.
