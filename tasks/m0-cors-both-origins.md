# Task: m0-cors-both-origins — gcloud.sh must allow BOTH run.app URL forms

**Milestone:** M0 polish (human-found, second CORS incident) · **Type:** infra codify · **Slug:** `m0-cors-both-origins`

## Problem

Cloud Run services answer on two URL forms simultaneously:
- legacy hash form: `https://blueshift-app-rv23it3sgq-uc.a.run.app` (what `status.url` reports)
- deterministic form: `https://blueshift-app-<project_number>.<region>.run.app`

`deploy/gcloud.sh` auto-resolves the CORS origin from `status.url` only, so the bucket
allowlist contained just the hash form while the human browsed the deterministic form →
preflight blocked → AC1 upload failed a second time (2026-07-23). The Architect has
already applied the both-origins config operationally and verified preflights for both
origins return matching `access-control-allow-origin`.

## Scope

1. **deploy/gcloud.sh:** origin resolution must emit BOTH forms — `status.url` as
   resolved today AND the deterministic form constructed from
   `https://${SERVICE}-${PROJECT_NUMBER}.${REGION}.run.app` (project number looked up via
   `gcloud projects describe --format='value(projectNumber)'`; reuse if already fetched).
   De-duplicate if equal. Keep the CORS_ORIGINS array structure so a custom domain stays
   a one-line addition. The generated JSON must match what is live now (methods
   PUT/POST/GET/HEAD; responseHeader Content-Type, x-goog-resumable, Location; maxAge 3600).
2. **deploy/README.md:** one-sentence note that both URL forms must be in the CORS origin
   list and why.
3. **PUBLIC_BASE_URL:** the app now reads optional `PUBLIC_BASE_URL` (m0-upload-protocol)
   as the Origin fallback for non-browser callers of episode create. Set it in the Cloud
   Run service env (deploy/gcloud.sh and the deploy workflow's env block if one exists)
   to the deterministic URL form. One line; verify with `gcloud run services describe`.

## Acceptance

- `bash -n deploy/gcloud.sh` passes; make check green (script-only change).
- Reviewer verifies: both forms present in the generated origins for default config, no
  duplicate entries when forms coincide, README note present.

## Evidence

Summary; diff; sample of the generated CORS JSON.
