# Task: m0-ci-deploy — CI green-gates for real, image, staging/prod pipeline

**Milestone:** M0 (docs/SPEC-M0.md §10) · **Type:** build/CI/deploy · **Slug:** `m0-ci-deploy`

## Goal

`pr.yml` actually runs the full gate suite (including DB-backed tests and e2e) and blocks
merges; one Docker image serves app + worker; `deploy.yml` ships staging on main and supports
manual prod promote. Human-owned GCP steps are clearly separated and listed.

## Human prerequisites (Architect flags these to the human; NOT implementer work)

1. Create the GitHub repo + push (no remote exists yet); enable branch protection on `main`
   requiring the `pr` workflow.
2. Run `deploy/gcloud.sh` once (`PROJECT=… GITHUB_REPO=… ./deploy/gcloud.sh`).
3. Set repo config: `vars.GCP_PROJECT`, `vars.GCP_REGION`, `vars.STAGING_URL`,
   `secrets.GCP_WIF_PROVIDER`, `secrets.GCP_DEPLOY_SA`.
4. Identity Platform: enable email/password provider; create the first real user per
   `docs/RUNBOOK.md`; put `IDP_API_KEY` in Secret Manager.

## Scope (implementer)

1. **`Dockerfile`** (multi-stage, one image for app + worker):
   stage 1 node:22 → `npm ci && npm run build` (web); stage 2 golang:1.25 → copy web build
   into `internal/webembed/dist`, `go build ./cmd/app ./cmd/worker`; stage 3 runtime
   (debian-slim) → install **ffmpeg** only, non-root user, both binaries, `ENTRYPOINT ["/app/app"]`
   (worker runs via `command: ["/app/worker"]` + args on the Cloud Run Job). Local proof:
   `docker build` + run `/healthz` (if Docker present locally; otherwise CI proves it).
2. **`pr.yml` hardening:** add Postgres 18 service container (pgvector image) +
   `TEST_DATABASE_URL` so the DB-backed store/migration tests RUN in CI (no silent skips —
   add a grep on test output asserting they weren't skipped); install ffmpeg; keep
   make check / eval / e2e steps; e2e uses the demo webServer path from m0-demo-seed
   (Postgres via the service container: support `DEMO_DATABASE_URL`).
3. **`deploy.yml` completion:** verify the existing staging/promote jobs against the real
   artifacts (image name, migrate job invocation — add the `blueshift-migrate*` Cloud Run
   Jobs wiring or a migration step that runs `migrate` against Cloud SQL via the auth proxy;
   choose the simplest that matches `deploy/gcloud.sh`, and update `deploy/gcloud.sh`
   consistently if it lacks the migrate job). Keep §7 semantics: staging on push to main →
   e2e against staging → rc tag; manual promote → prod --no-traffic → migrations → 10% →
   watch → 100%.
4. **Worker trigger in cloud:** confirm envs the app needs on Cloud Run
   (`WORKER_TRIGGER=cloudrun`, job name/region) are declared in deploy.yml/gcloud.sh.
5. **Config docs:** deploy/README or comments listing every env/secret each service needs.

## Out of scope

Actually running gcloud/gh against real infrastructure (human does that), the deliberate
failure proofs (m0-gate-proofs), monitoring/alerting beyond what exists.

## Acceptance

- `make check` green; `docker build` succeeds (locally or reasoned-through if no Docker —
  state honestly; CI will prove it after push).
- `pr.yml` as committed would run: gates + DB-backed tests (not skipped) + eval + e2e on a
  PR, and fails the run if any step is red.
- `deploy.yml` and `deploy/gcloud.sh` are mutually consistent (image path, job names,
  migrate mechanism, env/secret names) — the Reviewer must cross-check them line by line.

## Evidence to return

Summary + deviations; diffstat; tail of `make check`; docker build output tail (or the gap);
a table of service → env/secret → source; open questions.
