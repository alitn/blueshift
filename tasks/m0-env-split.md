# Task: m0-env-split — staging and prod in separate GCP projects

**Milestone:** M0 (pre-provisioning; see docs/ENVIRONMENTS.md) · **Type:** CI/deploy · **Slug:** `m0-env-split`

## Ruling

One GCP project per cloud environment (`blueshift-staging`, `blueshift-prod` — exact ids
are the human's choice via vars). Local dev and CI stay GCP-free. docs/ENVIRONMENTS.md is
the rationale; this task implements §"What this changes in the repo".

## Scope

1. **`.github/workflows/deploy.yml`:**
   - Replace `vars.GCP_PROJECT` with `vars.GCP_PROJECT_STAGING` / `vars.GCP_PROJECT_PROD`;
     auth via `secrets.GCP_WIF_PROVIDER_STAGING|_PROD` + `secrets.GCP_DEPLOY_SA_STAGING|_PROD`.
   - Staging job: build once → push to the **staging** project's Artifact Registry →
     deploy staging services (now named without `-staging` suffix, since the project scopes
     them) → migrations via proxy against staging Cloud SQL → smoke → rc tag.
   - Promote job: **copy the image by digest** from staging AR to prod AR
     (`gcloud container images add-tag` / `gcrane cp` — pick the simplest supported for
     Artifact Registry cross-project copy with WIF auth), then the existing §7 flow against
     the prod project. Same digest tested = same digest shipped; never rebuild on promote.
   - Jobs no-op cleanly when the respective vars are unset (safe to land before provisioning).
2. **`deploy/gcloud.sh`:** remove `-staging` naming assumptions (script is already
   per-PROJECT); ensure Speech/Vertex quota-cap + budget-alert steps exist per project (add
   a documented manual step if budgets can't be created idempotently from gcloud CLI);
   header usage examples show running it once per project.
3. **`deploy/README.md`:** env/secret matrix per project; the human-prerequisite list
   updated (two gcloud.sh runs, per-project vars/secrets).
4. **`tasks/m0-ci-deploy.md` is superseded on these points** — do not edit it (history);
   your README is the current source of truth.

## Out of scope

Actually creating projects (human), pr.yml (no GCP), app code, a sandbox project.

## Acceptance

- `make check` green; both workflow YAMLs parse; `bash -n deploy/gcloud.sh` clean.
- Reviewer cross-checks deploy.yml ↔ gcloud.sh ↔ README consistency for BOTH projects
  (names, WIF secrets, registries, SQL instances, secret ids).
- Promote provably ships the staging-tested digest (no rebuild step present).

## Evidence to return

Summary + deviations; diffstat; tail of make check; YAML/bash validation output; the updated
human-prerequisite list; open questions.
