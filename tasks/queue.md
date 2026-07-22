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
| m0-db-baseline | migration 0001 + sqlc + ids codec | committed (427e62d) | tasks/m0-db-baseline.md |
| m0-check-hardening | make check fails on any red step | committed (82fe99b) | tasks/m0-check-hardening.md |
| m0-web-skeleton | SvelteKit + tokens + ui primitives | committed (27ac4f9) | tasks/m0-web-skeleton.md |
| m0-auth | Identity Platform + authz middleware | committed (fc20b53) | tasks/m0-auth.md |
| m0-upload | signed upload → GCS + episode create | committed (99f1acc) | tasks/m0-upload.md |
| m0-worker-ingest | worker Job: audio + proxy + status | committed (4e3e582) | tasks/m0-worker-ingest.md |
| m0-library | Library page, live status, playback | spec-ready | tasks/m0-library.md |
| m0-demo-seed | make demo/dev + e2e harness + baselines | spec-ready | tasks/m0-demo-seed.md |
| m0-ci-deploy | CI gates live + staging/prod pipeline | spec-ready | tasks/m0-ci-deploy.md |
| m0-gate-proofs | Deliberate-failure proofs (AC 2/3/4/6) | spec-ready | tasks/m0-gate-proofs.md |

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
- 2026-07-22 — m0-db-baseline: APPROVE first pass, committed 427e62d (7 deviations accepted:
  go 1.25 directive, orgs/shows public_id, NULLS NOT DISTINCT config unique, etc.). Implementer
  found make check exit-code hole → m0-check-hardening inserted, APPROVE, committed 82fe99b
  (proof: seeded vet+lint failures each fail the gate). Scaffold gates/CI committed da5a0a4
  (were untracked since initial commit).
- 2026-07-22 — m0-web-skeleton: REJECT cycle 1 (tracked build artifact under webembed/dist),
  fixed, APPROVE cycle 2, committed 27ac4f9. Screenshots in .artifacts/screens/m0-web-skeleton/.
  Specs written for m0-auth, m0-upload, m0-worker-ingest, m0-library.
- 2026-07-22 — m0-auth: REJECT cycle 1 (IDP API key could leak into logs via url.Error on
  transport failure — Reviewer catch), fixed + regression test, APPROVE cycle 2, committed
  fc20b53. Accepted deviations: email as session subject (users have no public_id — closed
  prefix registry), org name-only in /me.
- 2026-07-22 — m0-upload: APPROVE first pass, committed 99f1acc. blob seam (gcs/localdir),
  org_ id prefix added, migration 0003 (master_size_bytes, additive). Reviewer note (non-
  blocking): local PUT lacks MaxBytesReader — dev-only seam, revisit if touched.
- 2026-07-22 — m0-worker-ingest: implementer cut off once by API spend limit, resumed from
  transcript, completed. APPROVE first pass, committed 4e3e582 (real-ffmpeg tests, process-
  group kill, CAS claim, neutral error_id). Accepted: no new migration needed; inline trigger
  dispatch; WORKER_TRIGGER default exec (deploy sets cloudrun). M1 backlog note: no reaper
  for episodes stuck in processing after a worker crash. Specs written for m0-demo-seed,
  m0-ci-deploy, m0-gate-proofs.
