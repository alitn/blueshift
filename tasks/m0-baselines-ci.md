# Task: m0-baselines-ci — one-shot CI workflow to generate visual baselines

**Milestone:** M0 (AC3 prerequisite) · **Type:** CI tooling · **Slug:** `m0-baselines-ci`

## Goal

Visual baselines must be generated on the CI Linux platform (committed darwin baselines would
false-fail CI). Provide a `workflow_dispatch`-only workflow that runs the Playwright suite
with `--update-snapshots` against the CI demo stack and uploads the resulting
`web/tests/__screenshots__/` as an artifact. The Architect then downloads and commits them
(the standing one-time authorization).

## Scope

1. `.github/workflows/baselines.yml`: `workflow_dispatch` only; ubuntu-latest; mirrors
   pr.yml's e2e environment exactly (pgvector service container, DEMO_DATABASE_URL, setup-bun
   pinned, setup-go, ffmpeg + migrate install, bun install frozen, playwright chromium via
   `bunx --bun=false playwright install --with-deps chromium`); runs
   `bunx --bun=false playwright test --update-snapshots` from web/ (webServer boots the demo);
   uploads `web/tests/__screenshots__/` as artifact `visual-baselines` (retention 7d); also
   uploads `web/test-results/` on failure. No push/commit from CI; contents: read.
2. Reuse pr.yml's steps verbatim where possible (comment pointing at pr.yml to keep them in
   lockstep).

## Acceptance

- YAML parses; `make check` green (no code paths touched).
- Reviewer cross-checks env parity with pr.yml's e2e job line by line (a drifted env would
  produce baselines that fail PR CI).

## Evidence

Summary; the workflow file; validation output.
