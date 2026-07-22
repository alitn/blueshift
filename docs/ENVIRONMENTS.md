# ENVIRONMENTS — dev / staging / prod separation

**Decision (Architect, 2026-07-22, researched):** one Google Cloud **project per cloud
environment**, because projects are Google's isolation boundary for data, IAM, quotas,
billing, and API enablement. Everything below maps Blueshift's pipeline onto that rule.
Buckets don't need clever naming schemes across envs — the project split does the work
(bucket names stay `{project}-media`, which is env-scoped by construction).

## The four environments

| Env | Where | Postgres | Blob | Auth | Worker | AI (ASR/LLM) |
|-----|-------|----------|------|------|--------|--------------|
| **local dev** | your machine (`make dev` / `make demo`) | local PG 18 + pgvector | `BLOB_MODE=local` dir | `AUTH_MODE=dev` | `WORKER_TRIGGER=exec` | **never live** — recorded fixtures (M1 policy) |
| **CI** | GitHub Actions | pgvector service container | local dir | dev | exec | recorded fixtures |
| **staging** | GCP project `blueshift-staging` | Cloud SQL (smallest tier) | `blueshift-staging-media` | Identity Platform (own user pool) | Cloud Run Job | live, quota-capped, budget-alerted |
| **prod** | GCP project `blueshift-prod` | Cloud SQL | `blueshift-prod-media` | Identity Platform | Cloud Run Job | live |

Rules that fall out of the project split automatically: separate service accounts and IAM
per env (never shared); separate Secret Manager secrets; separate Identity Platform user
pools (a staging test user can never sign into prod); separate Speech/Vertex quotas and
billing lines; deleting/experimenting in staging cannot touch prod data.

## Local dev is GCP-free by design

Postgres is local; blob store is a directory; auth is offline; the worker is a subprocess;
ASR/LLM calls are replayed from recorded fixtures (the `/internal/asr` + `/internal/llm`
record/replay policy in CLAUDE.md). Day-to-day development costs zero and works offline.

## Live-provider usage (Chirp etc.) — where and when

- **Never from a developer laptop against prod.** Live ASR/LLM runs happen in exactly two
  places: (1) **staging**, exercised by the deploy pipeline and the nightly live smoke on
  one fixture (M1); (2) **fixture recording**: when a new recorded fixture is needed, run
  the recorder against **staging's** project credentials, commit the sanitized fixture.
- Both cloud projects get **budget alerts** and explicit Speech/Vertex quota caps at
  provisioning time; staging's caps are tight (it only ever processes fixtures and demo
  episodes).
- No third "sandbox" project for now (Occam): staging doubles as the live-API playground.
  If experiments ever risk destabilizing staging demos, split a sandbox project then.

## What this changes in the repo (task: `m0-env-split`)

Today `deploy.yml` deploys `-staging`-suffixed services into the *same* project as prod.
The split requires:

1. `deploy.yml`: `vars.GCP_PROJECT_STAGING` + `vars.GCP_PROJECT_PROD` (replacing the single
   `GCP_PROJECT`); staging jobs target the staging project, promote targets prod; the image
   is built once, pushed to the staging registry, and **copied** to prod's registry on
   promote (same digest — what you tested is what you ship).
2. `deploy/gcloud.sh`: already parameterized by `PROJECT`; drop the `-staging` service-name
   suffixes (each project hosts identically-named services); WIF: one pool per project,
   both trusting the same GitHub repo, `secrets.GCP_WIF_PROVIDER_STAGING/_PROD` +
   `secrets.GCP_DEPLOY_SA_STAGING/_PROD`.
3. `deploy/README.md`: matrix updated per env.
4. Human runs `gcloud.sh` **twice** (once per project) instead of once.

## Cost notes (pilot scale)

Two Cloud SQL instances is the main duplicated cost (~2× db-g1-small). Acceptable for
isolation; if it stings, staging's instance can be stopped outside working hours (Cloud
SQL supports on-demand activation) — never share one instance across envs.

## Sources

- [Google: creating separate dev environments](https://cloud.google.com/appengine/docs/legacy/standard/php/creating-separate-dev-environments) — "projects provide complete isolation of code and data; operator permissions managed separately"
- [Planning your cloud projects](https://docs.cloud.google.com/endpoints/docs/grpc/planning-cloud-projects) — project-per-env naming (`web-app-dev`, `web-app-prod`)
- [Speech-to-Text pricing](https://cloud.google.com/speech-to-text/pricing) — per-minute billing; why dev replay-not-live matters
