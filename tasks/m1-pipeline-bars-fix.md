# Task: m1-pipeline-bars-fix — pipeline column must show stage truth

**Milestone:** M1 (human-found UI bug) · **Type:** web tiny · **Slug:** `m1-pipeline-bars-fix`

## Problem (human report, 2026-07-23)

On a READY episode the pipeline column renders five visually identical grey bars. Per
design/DESIGN.md the column must distinguish three bar states: **completed** (accent),
**pending/next** (grey), and **not-reached** (darker grey). A READY episode in today's
one-stage pipeline must show bar 1 (ingest) completed and bars 2–5 not-reached.

## Scope

1. Read design/DESIGN.md (and design/screens/ if present) for the pipeline column's
   exact bar states and tokens — the design is the truth; tokens only, no raw hex.
2. Fix the pipelineView mapping in web/src/lib/pipeline.ts (+ PipelineSteps rendering)
   so each display state produces the correct per-bar states:
   ready → [done, unreached×4]; processing → [active/pending, unreached×4];
   uploaded/queued → [pending, unreached×4]; awaiting_upload → all unreached (verify
   against design; if design says otherwise, follow design); failed → per design.
3. Tests: extend the vitest mapping tests per state; token-conformance assertion that
   the three bar styles use three distinct token-derived styles.
4. Screenshot evidence to .artifacts/screens/m1-pipeline-bars-fix/ at 1440×900 (Library
   with a READY row). Visual baselines: if committed baselines change, STOP and report
   — baseline updates are Architect-authorized only.

## Acceptance

- Targeted checks green (affected vitest, svelte-check, eslint) + full make check before
  review. Tiny-diff review path applies if ≤~20 changed lines of logic.
- Reviewer verifies bar states against design/DESIGN.md and the screenshot.

## Evidence

Summary; diff; screenshots; test transcript.
