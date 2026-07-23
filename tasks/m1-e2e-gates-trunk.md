# Task: m1-e2e-gates-trunk — Playwright e2e must gate the trunk, not just PRs

**Milestone:** M1 (process fix, Architect-directed 2026-07-23) · **Type:** CI/gate · **Slug:** `m1-e2e-gates-trunk`

## Why

The transcribe stage merged to main with a red Playwright e2e suite (upload→READY
timeout + stale token-conformance) and shipped to prod. It slipped because: (a) `make
check` — the pre-commit/commit-gate — excludes Playwright e2e; (b) the loop pushes
directly to main, so `pr.yml`'s `e2e` job (which DOES run Playwright) never runs on the
actual commits. So AC5 (upload-to-Ready) and the visual gate (AC3) have not actually been
guarding the trunk since M0's proof PR. `make check` GREEN is currently NOT sufficient to
prove the app works end to end.

## Options (Architect to choose in the spec; implementer executes the chosen one)

**Chosen: Option B — an e2e gate on push-to-main that blocks the rollout, fail-closed.**
Add an `e2e` job to `deploy.yml` (or a dedicated `main-e2e.yml` that the rollout depends
on) that stands up the demo stack (reuse pr.yml's postgres service + demo flow) and runs
the Playwright projects (flow, visual, token-conformance, rtl, axe). The rollout job
`needs:` it; a red e2e fails the deploy (fail-closed), so a stage/UI change that breaks
the golden path can never reach prod again. Keep it parallel with the existing check so
wall-clock stays ~the e2e duration (~2-3 min), matching the CI-speed budget.

(Option A — run `make e2e` inside the pre-commit hook — rejected: adds 2-3 min to every
commit including docs/infra, too heavy. Option C — require PRs for all merges — rejected:
changes the whole operating model. B gates the trunk without slowing commits or changing
the loop.)

## Scope

1. Add the e2e job (demo stack + Playwright, all projects incl. visual regression against
   committed baselines) triggered on push to main, before/gating the rollout.
2. Rollout `needs: [e2e, ...]`; fail-closed (any red e2e ⇒ no deploy). Preserve the
   existing concurrency group + WATCH minutes behaviour.
3. Ensure baseline (visual) comparison runs here too, so a UI drift blocks the deploy
   (not just a manually-triggered baselines run).
4. Keep the deploy path filters sane (docs-only pushes should early-exit e2e like pr.yml
   already does for its e2e).
5. Document the gate in docs/ENVIRONMENTS.md (Architect applies wording).

## Acceptance

- A deliberately-broken golden-path change (seeded in a throwaway branch/PR or simulated)
  would fail the new gate and block the rollout — demonstrate the wiring proves this
  (e.g. show the job runs Playwright and the rollout `needs` it).
- make check green; workflow YAML valid; no change to the fast path for docs-only pushes.

## Evidence

Summary; workflow diffs; a dry-run/log showing e2e runs and gates the rollout; open questions.
