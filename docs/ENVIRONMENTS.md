# ENVIRONMENTS — dev / staging / prod separation

**Decision (human, 2026-07-22 — PoC scope):** **one Google Cloud project** hosting prod,
plus fully local dev; **no staging environment or staging CD for now**. Inside the single
project, dev is separated from prod by *service accounts and buckets*: the
`dev-experiments@` SA can touch only `<project>-media-dev` and the Speech/Vertex APIs —
never Cloud SQL, the prod bucket, or Cloud Run. Full CI gates on PRs are unchanged; pushes
to main deploy prod through the progressive rollout (no-traffic → migrate → smoke → 10% →
watch → 100%).

*Scale-up path (recorded, not active):* the researched best practice is one project per
environment — projects are Google's isolation boundary for data, IAM, quotas, and billing.
The two-project layout was implemented once (see git history, `m0-env-split`) and can be
restored when the PoC graduates.

## The three environments (PoC)

| Env | Where | Postgres | Blob | Auth | Worker | AI (ASR/LLM) |
|-----|-------|----------|------|------|--------|--------------|
| **local dev** | your machine (`make dev` / `make demo`) | local PG 18 + pgvector | `BLOB_MODE=local` dir | `AUTH_MODE=dev` | `WORKER_TRIGGER=exec` (local subprocess) | recorded fixtures by default; fixture capture via `dev-experiments@` SA + `<project>-media-dev` |
| **CI** | GitHub Actions | pgvector service container | local dir | dev | exec | recorded fixtures |
| **prod** | the single GCP project | Cloud SQL | `<project>-media` | Identity Platform | Cloud Run Job | live |

Separation inside the one project: the dev SA holds only dev-bucket + Speech/Vertex scopes;
the prod runtime SA is bound to Cloud Run and its credentials never leave it; laptops never
hold prod credentials.

## Local dev is GCP-free by design

Postgres is local; blob store is a directory; auth is offline; the worker is a subprocess;
ASR/LLM calls are replayed from recorded fixtures (the `/internal/asr` + `/internal/llm`
record/replay policy in CLAUDE.md). Day-to-day development costs zero and works offline.

## Live-provider usage (Chirp etc.) — where and when

- **Never from a developer laptop with prod credentials.** Live ASR/LLM in dev = the
  `dev-experiments@` SA only: local worker uploads the extracted `audio.flac` to
  `<project>-media-dev`, calls the API, stores the transcript in local Postgres, deletes
  the temp object. Default remains recorded-fixture replay (offline, free, deterministic);
  live calls are for fixture capture and deliberate experiments.
- The project gets a **budget alert** and explicit Speech/Vertex quota caps at
  provisioning time (see `deploy/gcloud.sh` guardrails section).
- Prod's nightly live smoke on one fixture (M1) runs in the prod service, not from laptops.

## Repo mapping (task: `m0-single-project`)

`deploy.yml`: single `vars.GCP_PROJECT`; push to main → build → prod rollout
(no-traffic → migrations → candidate smoke → 10% → watch → 100%). `deploy/gcloud.sh`: one
run; provisions prod resources + the dev SA/bucket + guardrails. `deploy/README.md`
carries the per-service env/secret matrix and the (short) human-prerequisite list.

## Cost notes (PoC scale)

One Cloud SQL instance, one bucket + a dev bucket (pennies), API usage metered by the
quota caps. The dominant cost lever is ASR minutes — which the fixture-replay default
keeps near zero outside real usage.

## Sources

- [Google: creating separate dev environments](https://cloud.google.com/appengine/docs/legacy/standard/php/creating-separate-dev-environments) — "projects provide complete isolation of code and data; operator permissions managed separately"
- [Planning your cloud projects](https://docs.cloud.google.com/endpoints/docs/grpc/planning-cloud-projects) — project-per-env naming (`web-app-dev`, `web-app-prod`)
- [Speech-to-Text pricing](https://cloud.google.com/speech-to-text/pricing) — per-minute billing; why dev replay-not-live matters
