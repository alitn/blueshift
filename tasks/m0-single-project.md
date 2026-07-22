# Task: m0-single-project — PoC deploy: one GCP project, dev SA, no staging pipeline

**Milestone:** M0 (human ruling 2026-07-22; supersedes m0-env-split's two-project layout) · **Type:** CI/deploy · **Slug:** `m0-single-project`

## Ruling

PoC scope: **one** GCP project hosting prod; local dev is GCP-free except a **dev service
account + dev bucket** used only for future ASR/LLM fixture capture. No staging environment,
no staging CD. Full CI gates on PRs stay exactly as they are (pr.yml untouched).

## Scope

1. **`.github/workflows/deploy.yml`:** collapse to single-project:
   - Vars/secrets: back to `vars.GCP_PROJECT`, `vars.GCP_REGION`, `secrets.GCP_WIF_PROVIDER`,
     `secrets.GCP_DEPLOY_SA`. Remove `_STAGING/_PROD` pairs and `STAGING_URL`.
   - Push to main → build + push image → **deploy prod with `--no-traffic`** → migrations
     (auth-proxy step, unchanged mechanics) → candidate-tag smoke (healthz/readyz//,
     candidate tag URL) → 10% → watch (existing candidate-URL + error-reporting logic) →
     100%. I.e., main auto-deploys THROUGH the progressive rollout — the manual
     `workflow_dispatch` promote job disappears; keep a manual `workflow_dispatch` input to
     re-run/rollback traffic if trivially expressible, else document rollback command only.
   - No cross-project image copy (single registry). Jobs no-op until `vars.GCP_PROJECT` set.
2. **`deploy/gcloud.sh`:** drop `ENV_TIER` (single run again); keep budget/quota guardrail
   section; **add dev-experiment resources**: `dev-experiments@<project>` SA with access
   ONLY to a new `<project>-media-dev` bucket + Speech/Vertex invocation scopes (no SQL, no
   prod bucket, no Run); print how to mint local ADC creds for it
   (`gcloud auth application-default login --impersonate-service-account=…` guidance).
   Prod bucket/IAM unchanged.
3. **`deploy/README.md`:** rewrite matrix for one project + dev SA; human-prerequisite list
   re-cut (one project, one gcloud.sh run, 4 GitHub settings: GCP_PROJECT, GCP_REGION,
   GCP_WIF_PROVIDER, GCP_DEPLOY_SA).
4. Do not touch docs/ENVIRONMENTS.md (Architect updates it separately).

## Acceptance

- `make check` green; workflow YAMLs parse; `bash -n` clean.
- Reviewer cross-checks deploy.yml ↔ gcloud.sh ↔ README consistency (names, secrets, SA
  roles — especially that dev-experiments SA cannot touch prod data).
- Rollout on main preserves §7 safety order (no-traffic → migrate → smoke → 10% → watch → 100%).

## Evidence

Summary + deviations; diffstat; make check tail; validation output; final human-prereq list.
