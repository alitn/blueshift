# Deploy — configuration reference

**PoC scope: one GCP project** hosts prod (the exact id is the human's choice via
`vars.GCP_PROJECT`); see `docs/ENVIRONMENTS.md` for the rationale and the recorded
scale-up path (one project per environment, restorable when the PoC graduates).
Local dev and CI are GCP-free. Inside the single project, dev work is separated
from prod by a dedicated `dev-experiments@` service account and a
`<project>-media-dev` bucket used only for local ASR/LLM fixture capture — never
Cloud SQL, never the prod bucket, never Cloud Run.

One image (`Dockerfile`) ships both binaries. `deploy/gcloud.sh` provisions the
durable Google Cloud infrastructure **once** (idempotent);
`.github/workflows/deploy.yml` builds the image and rolls it out on every push to
`main` through the progressive rollout. The Cloud Run service + worker Job are
`blueshift-app` and `blueshift-worker`. Migrations run from the deploy workflow
through the Cloud SQL Auth Proxy against the repo `migrations/` tree — there is no
separate migrate binary or Job.

All env vars are read by `internal/config` (`config.Load`); the validation rules
there are the source of truth for what is required in which `ENV`.

## The project at a glance

| Concern               | value                                               |
| --------------------- | --------------------------------------------------- |
| Cloud Run service     | `blueshift-app`                                      |
| Cloud Run Job         | `blueshift-worker`                                   |
| Artifact Registry     | `<region>-docker.pkg.dev/<project>/blueshift`        |
| Cloud SQL instance    | `<project>:<region>:blueshift-pg`                    |
| Prod GCS bucket       | `<project>-media`                                   |
| Dev scratch bucket    | `<project>-media-dev` (fixture capture only)         |
| WIF provider (secret) | `GCP_WIF_PROVIDER`                                   |
| Deploy SA (secret)    | `GCP_DEPLOY_SA` → `deployer@<project>...`            |
| Runtime SA            | `app-runtime@<project>.iam.gserviceaccount.com`      |
| Dev SA                | `dev-experiments@<project>.iam.gserviceaccount.com`  |
| Secret ids            | `database-url`, `session-signing-key`, `identity-platform-config` |
| Live AI               | quota-capped + budget alert                          |

The image is **built once per push** in the rollout job and pushed to the
project's registry, then deployed `--no-traffic` as a `candidate` tag before any
traffic shift. There is no cross-project copy and no manual promote — what `main`
builds is exactly what prod runs, gated by the smoke + watch.

## Secrets (Secret Manager → env var)

Created empty by `deploy/gcloud.sh`; values are filled by the human (see
`docs/RUNBOOK.md`). Never printed to client-visible surfaces.

| Secret id                  | Injected as      | Contents                                             |
| -------------------------- | ---------------- | ---------------------------------------------------- |
| `database-url`             | `DATABASE_URL`   | `postgres://app:…@/blueshift?host=/cloudsql/<inst>` (unix-socket DSN) |
| `session-signing-key`      | `SESSION_SECRET` | random 32B+; signs session cookies and upload tokens |
| `identity-platform-config` | `IDP_API_KEY`    | Identity Platform web API key (server-side sign-in)  |

## Service-account IAM (granted by `deploy/gcloud.sh`)

| SA                         | Roles                                                                                                                                              | Why                                                                          |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| `app-runtime@<project>`    | `cloudsql.client`, `storage.objectAdmin`, `aiplatform.user`, `speech.client`, `secretmanager.secretAccessor`, `logging.logWriter`, `errorreporting.writer`, **`run.invoker`** (project) + **`iam.serviceAccountTokenCreator` on _itself_** (SA-scoped) | DB, blob, AI, secrets, logs; `run.invoker` so the app can execute the worker Job (`jobs/{job}:run`); the self-scoped `serviceAccountTokenCreator` grants `iam.serviceAccounts.signBlob` so Cloud Run can mint V4 signed URLs (master upload, proxy playback) with no private key — without it `POST /api/episodes` 503s on a signing 403 |
| `deployer@<project>`       | `run.admin`, `artifactregistry.writer`, `iam.serviceAccountUser`, `cloudsql.client`, **`errorreporting.viewer`**, + `secretmanager.secretAccessor` on **only** `database-url` | Build/deploy service+jobs, act as runtime SA, run `migrate up` via auth proxy, and read Error Reporting during the rollout watch |
| `dev-experiments@<project>`| `aiplatform.user`, `speech.client` (project-scoped invocation) + `storage.objectAdmin` on **only** `<project>-media-dev` (bucket-scoped) | Local ASR/LLM fixture capture. **No Cloud SQL, no Cloud Run, no access to the prod bucket** — the storage binding is scoped to the dev bucket alone and no project-level storage role is granted. |

The worker-Job execute permission is project-scoped `run.invoker` rather than a
per-Job binding: the worker Jobs do not exist when `gcloud.sh` runs (deploy.yml
creates them), so a job-scoped binding could not be applied idempotently there.

The signing grant is deliberately **SA-scoped, not project-wide**: `gcloud.sh`
adds `app-runtime` as a member with `roles/iam.serviceAccountTokenCreator` on the
`app-runtime` SA's _own_ IAM policy (`gcloud iam service-accounts
add-iam-policy-binding app-runtime@… --member=serviceAccount:app-runtime@…`), so
only that SA can sign as itself and no other identity gains signing power. This is
what lets the storage client produce V4 signatures via the IAM `signBlob` API
without a downloaded key; the symptom when it is missing is a signing `403` in the
runtime logs surfacing as a neutral `503` from `POST /api/episodes`.

**Dev-SA isolation.** `dev-experiments@` exists only for laptop fixture capture
and has **no WIF binding** (it is never used by CI). Developers mint short-lived
local ADC by impersonating it — no JSON keys leave Google:

```
# one-time (owner grants a developer impersonation on the dev SA):
gcloud iam service-accounts add-iam-policy-binding \
  dev-experiments@<project>.iam.gserviceaccount.com \
  --member=user:<you@example.com> --role=roles/iam.serviceAccountTokenCreator
# per session (mint local ADC that impersonates the dev SA):
gcloud auth application-default login \
  --impersonate-service-account=dev-experiments@<project>.iam.gserviceaccount.com
```

Default local development stays on recorded fixtures and needs none of this
(`docs/ENVIRONMENTS.md`).

## Cloud Run service — `blueshift-app`

ENTRYPOINT `/app/app`. Serves the embedded SPA + `/api`, `/healthz`, `/readyz`.

| Env var             | Value                              | Source (deploy.yml)          |
| ------------------- | ---------------------------------- | ---------------------------- |
| `ENV`               | `prod`                             | `--set-env-vars`             |
| `AUTH_MODE`         | `identity`                         | `--set-env-vars`             |
| `WORKER_TRIGGER`    | `cloudrun`                         | `--set-env-vars`             |
| `WORKER_JOB_NAME`   | `blueshift-worker`                 | `--set-env-vars`             |
| `WORKER_JOB_REGION` | `<region>`                         | `--set-env-vars`             |
| `WORKER_JOB_PROJECT`| `<project>`                        | `--set-env-vars`             |
| `BLOB_MODE`         | `gcs`                              | `--set-env-vars`             |
| `GCS_BUCKET`        | `<project>-media`                  | `--set-env-vars`             |
| `PORT`              | injected by Cloud Run              | platform (config reads it)   |
| `DATABASE_URL`      | secret `database-url`              | `--set-secrets`              |
| `SESSION_SECRET`    | secret `session-signing-key`       | `--set-secrets`              |
| `IDP_API_KEY`       | secret `identity-platform-config`  | `--set-secrets`              |

Also: `--add-cloudsql-instances <project>:<region>:blueshift-pg`,
`--service-account app-runtime@<project>.iam.gserviceaccount.com`.

`WORKER_TRIGGER=cloudrun` makes the app start the worker Job (name/region/project
above) instead of spawning a local process; those three vars are required by
`config` whenever the trigger is `cloudrun`.

## Cloud Run Job — `blueshift-worker`

Same image, `--command /app/worker`, invoked per stage as
`<episode_public_id> <stage>` (args supplied at execution time).

| Env var          | Value                             | Source (deploy.yml) |
| ---------------- | --------------------------------- | ------------------- |
| `ENV`            | `prod`                            | `--set-env-vars`    |
| `AUTH_MODE`      | `identity`                        | `--set-env-vars`    |
| `BLOB_MODE`      | `gcs`                             | `--set-env-vars`    |
| `GCS_BUCKET`     | `<project>-media`                 | `--set-env-vars`    |
| `DATABASE_URL`   | secret `database-url`             | `--set-secrets`     |
| `SESSION_SECRET` | secret `session-signing-key`      | `--set-secrets`     |
| `IDP_API_KEY`    | secret `identity-platform-config` | `--set-secrets`     |

Also: `--set-cloudsql-instances`, `--service-account app-runtime`,
`--max-retries 2 --task-timeout 3600`.

The worker never authenticates, but `config.Load()` validates the auth env in a
prod-like `ENV` (identity mode requires `SESSION_SECRET` and `IDP_API_KEY`), so
the Job carries the same three secrets as the service.

## Rollout — `main` auto-deploys through the progressive rollout

Every push to `main` runs one `rollout` job that follows the §7 safety order:

1. **Build** the image once and push it to the project registry (`$IMAGE:<sha>`).
2. **Deploy** `blueshift-app` **`--no-traffic --tag candidate`** and update
   `blueshift-worker` (same image).
3. **Migrate** (additive-only) via the Cloud SQL Auth Proxy.
4. **Smoke the candidate tag url** (`/readyz` 200, `/` 200 `text/html` non-empty):
   at `--no-traffic` the base url still serves the previous revision, so only the
   tag url exercises the new code. The rollout gates on this smoke.
5. Shift **10%** to the candidate, **watch ~10 min** (candidate-tag `/readyz` +
   Error Reporting), then shift **100%**.

There is no `docker build` outside step 1.

**Two `*.run.app` platform quirks the rollout accommodates.** (a) Google Frontend
intercepts `/healthz` at the edge and returns its own 404, so the smoke and watch
gate on **`/readyz`** (which also verifies the DB); `/healthz` still exists in the
app for local/internal use. (b) The org enforces domain-restricted sharing, which
forbids `allUsers` IAM bindings, so `blueshift-app` deploys with
**`--no-invoker-iam-check`** rather than a public invoker binding — the app does
its own auth.

**First-ever deploy (bootstrap).** The very first push creates `blueshift-app`
from nothing, where the §7 order needs two tweaks Cloud Run forces: `--no-traffic`
is rejected when *creating* a service, and there is no stable revision to split
10/90 against. A `Detect first-ever deploy` step therefore probes
`gcloud run services describe blueshift-app`; when the service is absent it sets
`bootstrap=true`, the candidate is deployed **without** `--no-traffic` (still
`--tag candidate`) so the sole revision serves 100% immediately, and the
10% → watch → 100% steps skip via their `if:` conditions. Migrations and the
candidate smoke still run and still gate the deploy (order: deploy → migrate →
smoke → done); on bootstrap the smoke targets the service base url since the only
revision already serves it. Every subsequent push takes the steady-state path
above unchanged.

**Rollback.** Re-point traffic to a known-good revision, either via the command
printed at the end of a successful run:

```
gcloud run services update-traffic blueshift-app --region <region> \
  --to-revisions <previous>=100
```

or by triggering the workflow manually (Actions → **deploy** → **Run workflow**)
with the revision name in the `rollback_to_revision` input — that runs a
traffic-only `rollback` job (no build, no migrate). Leaving the input blank on a
manual run re-runs a normal build + progressive rollout.

## CI migration step (deploy.yml)

| Value          | Source                                                        |
| -------------- | ------------------------------------------------------------- |
| `DATABASE_URL` | `gcloud secrets versions access latest --secret=database-url` |
| instance conn  | `<project>:<region>:blueshift-pg` via Cloud SQL Auth Proxy    |

The proxy binds a unix socket under `/cloudsql/<instance>` whose path matches the
`host=` in the `database-url` DSN, so `migrate -database "$DATABASE_URL" up`
connects with no DSN rewriting. Migrations are additive-only.

## Non-deployed environments (for reference)

- **`make demo` / `make dev`** (local, offline): `ENV=dev`, `AUTH_MODE=dev`,
  `BLOB_MODE=local`, `WORKER_TRIGGER=exec`, insecure demo `SESSION_SECRET` /
  `DEV_PASSWORD`; Postgres from `DEMO_DATABASE_URL` → docker fallback. Wiring in
  `tools/demo/lib.sh`.
- **CI `pr.yml`**: DB-backed store/migration tests use `TEST_DATABASE_URL`; the
  demo the e2e suite boots uses `DEMO_DATABASE_URL` — both point at the pgvector
  service container.

## Human prerequisites (run before the first deploy)

1. Create **one** GCP project (e.g. `blueshift-prod`), billing enabled.
2. Run `deploy/gcloud.sh` **once** (owner creds on the project):
   ```
   PROJECT=blueshift-prod GITHUB_REPO=<owner>/blueshift ./deploy/gcloud.sh
   ```
   Optionally pass `BILLING_ACCOUNT=<id> BUDGET_AMOUNT=<usd>` to script the budget
   alert; otherwise create it by hand (the script prints the exact manual step).
3. Complete the manual steps the script prints: enable Email/Password in Identity
   Platform; fill the three secret values; set the **budget alert** and
   **Speech-to-Text / Vertex AI quota caps** (sized to the pilot); map the domain
   for `blueshift-app`. Optionally grant a developer impersonation on
   `dev-experiments@` for local fixture capture.
4. Set **4** GitHub repo settings:
   - **variables:** `GCP_PROJECT`, `GCP_REGION`
   - **secrets:** `GCP_WIF_PROVIDER`, `GCP_DEPLOY_SA` (both printed by `gcloud.sh`)

The rollout job **no-ops** while `GCP_PROJECT` is empty — safe to land before the
project exists.
