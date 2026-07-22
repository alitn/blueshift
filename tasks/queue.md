# Task queue

Single source of truth for task state. Only the Architect edits this file. One task = one
spec file `tasks/<slug>.md` = one Implementer dispatch = one Reviewer verdict = one commit.

**States:** `queued → spec-ready → in-progress → in-review → approved → committed`
(plus `blocked(reason)` / `rejected(n)` for review cycles; 3 rejections escalate to human).

## M0 — Walking skeleton

| Slug | Task | State | Spec |
|------|------|-------|------|
| m0-scaffold | Repo scaffold, gates, docs (this) | in-review (human) | docs/SPEC-M0.md |
| m0-go-skeleton | app server, health, config, embed | queued | — |
| m0-db-baseline | migration 0001 + sqlc + ids codec | queued | — |
| m0-web-skeleton | SvelteKit + tokens + ui primitives | blocked(design/ export needed) | — |
| m0-auth | Identity Platform + authz middleware | queued | — |
| m0-upload | signed upload → GCS + episode create | queued | — |
| m0-worker-ingest | worker Job: audio + proxy + status | queued | — |
| m0-library | Library page, live status, playback | queued | — |
| m0-demo-seed | make demo/dev + e2e harness + baselines | queued | — |
| m0-ci-deploy | CI gates live + staging/prod pipeline | queued | — |

## Backlog

M1 decomposition happens after the M0 gate (see docs/SPEC-M1.md §Task decomposition).

## Log

- 2026-07-22 — scaffold created; awaiting human review before any M0 implementation.
