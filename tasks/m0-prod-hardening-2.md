# Task: m0-prod-hardening-2 — codify CORS + worker-trigger role (live fixes)

**Milestone:** M0 · **Type:** infra codify · **Slug:** `m0-prod-hardening-2`

## Live findings (human browser demo + Architect smoke)

1. Browser PUT/resumable-POST to GCS blocked: bucket had no CORS. Architect applied
   operationally: origin = the run.app URL, methods PUT/POST/GET/HEAD, headers
   Content-Type/x-goog-resumable/Location, maxAge 3600.
2. Worker trigger 403 `run.jobs.runWithOverrides` — roles/run.invoker lacks it (we pass arg
   overrides). Architect stopgap: job-scoped roles/run.developer (too broad to keep).

## Scope

1. **gcloud.sh:** (a) CORS config applied idempotently to the prod media bucket (origins:
   the run.app URL now; structure it so a future custom domain is a one-line addition);
   (b) create custom role `blueshiftWorkerRunner` (permissions: run.jobs.run,
   run.jobs.runWithOverrides) idempotently and bind it to app-runtime at PROJECT level
   (replaces the need for job-scoped bindings that can't exist pre-deploy). Keep
   roles/run.invoker (harmless, still needed for jobs.run path).
2. **deploy/README.md:** IAM table + a CORS note.
3. **Cleanup instruction (Architect will run):** print the exact command to REMOVE the
   stopgap job-scoped roles/run.developer binding once the custom role is live.

## Acceptance

- bash -n; make check green (script-only); reviewer verifies the custom role's permission
  list is exactly the two run.jobs perms (no wildcards), CORS JSON matches what's live.

## Evidence

Summary; diff; the cleanup command.
