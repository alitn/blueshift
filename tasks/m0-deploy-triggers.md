# Task: m0-deploy-triggers — deploy only when the runtime changed

**Milestone:** M0 · **Type:** CI fix · **Slug:** `m0-deploy-triggers`

## Finding (human-caught)

Every push to main triggers the full rollout incl. the 10-minute watch — 7 × ~13min today
for commits touching only docs/tasks/CI config. Wasteful (free-plan Actions minutes) and
noisy.

## Scope (deploy.yml only)

1. **Path-filter the push trigger:** deploy only when runtime-relevant paths change:
   `cmd/**, internal/**, migrations/**, web/** (EXCLUDING web/tests/**), go.mod, go.sum,
   Dockerfile, .dockerignore, deploy/**, .github/workflows/deploy.yml`. Everything else
   (docs/, tasks/, design/, *.md, .github/workflows/pr.yml etc.) skips. Use `paths:` on the
   push trigger (deploy has no required-check interaction, so plain paths is safe —
   workflow_dispatch remains as manual override for any case the filter misses).
2. **Watch duration tunable:** `WATCH_MINUTES` from `vars.WATCH_MINUTES` default **5**
   (PoC has no traffic; 5 min suffices; production-scale can set 10+ via the var without a
   code change). Loop count derives from it.
3. **Serialize rollouts:** `concurrency: { group: deploy-main, cancel-in-progress: false }`
   so stacked pushes queue instead of racing traffic steps (never cancel mid-rollout).

## Acceptance

- YAML parses; make check green (no code paths).
- Reviewer sanity-checks the path list against the repo layout (nothing runtime-relevant
  excluded; web/tests correctly excluded since test-only changes don't alter the shipped
  image... EXCEPT playwright.config/baselines don't ship — confirm web/tests exclusion is
  safe because Dockerfile stage 1 builds only from web src/config: verify what the image
  actually copies).

## Evidence

Summary; diff; the verified path-inclusion reasoning; validation.
