# Task queue

Single source of truth for task state. Only the Architect edits this file. One task = one
spec file `tasks/<slug>.md` = one Implementer dispatch = one Reviewer verdict = one commit.

**States:** `queued → spec-ready → in-progress → in-review → approved → committed`
(plus `blocked(reason)` / `rejected(n)` for review cycles; 3 rejections escalate to human).

## M0 — Walking skeleton

| Slug | Task | State | Spec |
|------|------|-------|------|
| m0-scaffold | Repo scaffold, gates, docs (this) | committed (human-approved) | docs/SPEC-M0.md |
| m0-design-contract | DESIGN.md transcribed from design export (Architect) | committed | design/DESIGN.md |
| m0-go-skeleton | app server, health, config, embed | committed (a59160c) | tasks/m0-go-skeleton.md |
| m0-db-baseline | migration 0001 + sqlc + ids codec | spec-ready | tasks/m0-db-baseline.md |
| m0-web-skeleton | SvelteKit + tokens + ui primitives | queued | — |
| m0-auth | Identity Platform + authz middleware | queued | — |
| m0-upload | signed upload → GCS + episode create | queued | — |
| m0-worker-ingest | worker Job: audio + proxy + status | queued | — |
| m0-library | Library page, live status, playback | queued | — |
| m0-demo-seed | make demo/dev + e2e harness + baselines | queued | — |
| m0-ci-deploy | CI gates live + staging/prod pipeline | queued | — |
| m0-gate-proofs | Deliberate-failure proofs (AC 2/3/4/6) | queued | — |

## Backlog

M1 decomposition happens after the M0 gate (see docs/SPEC-M1.md §Task decomposition).

## Log

- 2026-07-22 — scaffold created; awaiting human review before any M0 implementation.
- 2026-07-22 — human approved scaffold + M0 execution plan (T0–T10). Claude Design export
  found in design/project/; DESIGN.md transcribed by Architect (m0-design-contract), which
  unblocks m0-web-skeleton. design/screens/*.png still pending from human — until they land,
  the prototype HTML + DESIGN.md are the Reviewer's visual ground truth.
- 2026-07-22 — m0-go-skeleton: Implementer green on first pass; Reviewer APPROVE, no findings;
  committed a59160c. m0-db-baseline spec written (spec-ready).
