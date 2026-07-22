# Deploy — configuration reference

Staging and prod are **separate GCP projects** (`blueshift-staging`,
`blueshift-prod` — exact ids are the human's choice via `vars`). The project
boundary is Google's isolation unit for data, IAM, quotas, and billing; see
`docs/ENVIRONMENTS.md` for the rationale. Local dev and CI stay GCP-free.

One image (`Dockerfile`) ships both binaries. `deploy/gcloud.sh` provisions the
durable Google Cloud infrastructure once **per project** (idempotent — run it
twice, once for each project); `.github/workflows/deploy.yml` builds the image
and creates/updates the Cloud Run service + worker Job on every push to `main`
(staging) and on manual promote (prod). Because the project scopes them, both
projects host **identically-named** services — `blueshift-app` and
`blueshift-worker`, no `-staging` suffix. Migrations run from the deploy workflow
through the Cloud SQL Auth Proxy against the repo `migrations/` tree — there is
no separate migrate binary or Job.

All env vars are read by `internal/config` (`config.Load`); the validation rules
there are the source of truth for what is required in which `ENV`.

## Two projects at a glance

| Concern                | staging project (`GCP_PROJECT_STAGING`)          | prod project (`GCP_PROJECT_PROD`)              |
| ---------------------- | ------------------------------------------------ | ---------------------------------------------- |
| Cloud Run service      | `blueshift-app`                                  | `blueshift-app`                                |
| Cloud Run Job          | `blueshift-worker`                               | `blueshift-worker`                             |
| Artifact Registry      | `<region>-docker.pkg.dev/<staging>/blueshift`    | `<region>-docker.pkg.dev/<prod>/blueshift`     |
| Cloud SQL instance     | `<staging>:<region>:blueshift-pg`                | `<prod>:<region>:blueshift-pg`                 |
| GCS bucket             | `<staging>-media`                                | `<prod>-media`                                 |
| WIF provider (secret)  | `GCP_WIF_PROVIDER_STAGING`                        | `GCP_WIF_PROVIDER_PROD`                         |
| Deploy SA (secret)     | `GCP_DEPLOY_SA_STAGING`                           | `GCP_DEPLOY_SA_PROD`                            |
| Runtime SA             | `app-runtime@<staging>.iam.gserviceaccount.com`  | `app-runtime@<prod>.iam.gserviceaccount.com`   |
| Secret ids             | `database-url`, `session-signing-key`, `identity-platform-config` (same ids, separate values, separate Secret Manager) | same ids, separate values |
| Live AI                | quota-capped (tight) + budget alert              | quota-capped (pilot) + budget alert            |

The image is **built once** in the staging job and pushed to the staging
registry. On promote it is **copied by digest** into the prod registry (no
rebuild) — see "Promote" below.

## Secrets (Secret Manager → env var)

Created empty by `deploy/gcloud.sh` **in each project**; values are filled by the
human per project (see `docs/RUNBOOK.md`). Never printed to client-visible
surfaces. The secret ids are the same in both projects; the values are not, and
they live in each project's own Secret Manager.

| Secret id                  | Injected as      | Contents                                             |
| -------------------------- | ---------------- | ---------------------------------------------------- |
| `database-url`             | `DATABASE_URL`   | `postgres://app:…@/blueshift?host=/cloudsql/<inst>` (unix-socket DSN) |
| `session-signing-key`      | `SESSION_SECRET` | random 32B+; signs session cookies and upload tokens |
| `identity-platform-config` | `IDP_API_KEY`    | Identity Platform web API key (server-side sign-in)  |

## Service-account IAM (granted by `deploy/gcloud.sh`, per project)

Each project gets its own `app-runtime@` and `deployer@` SAs — never shared
across projects.

| SA                     | Roles                                                                                                                                              | Why                                                                          |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| `app-runtime@<project>`| `cloudsql.client`, `storage.objectAdmin`, `aiplatform.user`, `speech.client`, `secretmanager.secretAccessor`, `logging.logWriter`, `errorreporting.writer`, **`run.invoker`** | DB, blob, AI, secrets, logs — and `run.invoker` so the app can execute the worker Job (`jobs/{job}:run`) |
| `deployer@<project>`   | `run.admin`, `artifactregistry.writer`, `iam.serviceAccountUser`, `cloudsql.client`, **`errorreporting.viewer`**, + `secretmanager.secretAccessor` on **only** `database-url` | Build/deploy service+jobs, act as runtime SA, run `migrate up` via auth proxy, and read Error Reporting during the promote watch |

The worker-Job execute permission is project-scoped `run.invoker` rather than a
per-Job binding: the worker Jobs do not exist when `gcloud.sh` runs (deploy.yml
creates them), so a job-scoped binding could not be applied idempotently there.

**Cross-project note:** promote does NOT require any cross-project IAM binding.
The digest copy uses two project-scoped WIF auths in one job — pull as the
staging deployer, push as the prod deployer — so each project's `deployer@` only
ever touches its own registry.

## Cloud Run service — `blueshift-app` (same name in both projects)

ENTRYPOINT `/app/app`. Serves the embedded SPA + `/api`, `/healthz`, `/readyz`.

| Env var             | Value                              | Source (deploy.yml)          |
| ------------------- | ---------------------------------- | ---------------------------- |
| `ENV`               | `prod` / `staging`                 | `--set-env-vars`             |
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
`config` whenever the trigger is `cloudrun`. `WORKER_JOB_PROJECT` is always the
same project the service runs in — the app never triggers the other env's worker.

## Cloud Run Job — `blueshift-worker` (same name in both projects)

Same image, `--command /app/worker`, invoked per stage as
`<episode_public_id> <stage>` (args supplied at execution time).

| Env var          | Value                             | Source (deploy.yml) |
| ---------------- | --------------------------------- | ------------------- |
| `ENV`            | `prod` / `staging`                | `--set-env-vars`    |
| `AUTH_MODE`      | `identity`                        | `--set-env-vars`    |
| `BLOB_MODE`      | `gcs`                             | `--set-env-vars`    |
| `GCS_BUCKET`     | `<project>-media`                 | `--set-env-vars`    |
| `DATABASE_URL`   | secret `database-url`             | `--set-secrets`     |
| `SESSION_SECRET` | secret `session-signing-key`      | `--set-secrets`     |
| `IDP_API_KEY`    | secret `identity-platform-config` | `--set-secrets`     |

Also: `--add-cloudsql-instances`, `--service-account app-runtime`,
`--max-retries 2 --task-timeout 3600`.

The worker never authenticates, but `config.Load()` validates the auth env in a
prod-like `ENV` (identity mode requires `SESSION_SECRET` and `IDP_API_KEY`), so
the Job carries the same three secrets as the service.

## Promote — same digest tested, same digest shipped

The staging job builds the image once and pushes it to the **staging** registry.
The manual `workflow_dispatch` promote (input = the git SHA to ship) does **not**
rebuild:

1. Auth as the **staging** deployer, `docker pull` the tag, record its immutable
   digest.
2. Re-auth as the **prod** deployer, retag into the prod registry, `docker push`,
   then assert the pushed digest equals the recorded staging digest (content-
   addressed manifests guarantee this; the check makes it provable).
3. Deploy prod **by digest** (`$IMAGE@<digest>`), `--no-traffic --tag candidate`,
   run additive migrations, shift 10% → 10-min watch → 100%.

There is no `docker build` in the promote path — what staging tested is exactly
what prod runs.

## CI migration step (deploy.yml, staging + promote)

| Value          | Source                                                        |
| -------------- | ------------------------------------------------------------- |
| `DATABASE_URL` | `gcloud secrets versions access latest --secret=database-url` (in that job's project) |
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

1. Create **two** GCP projects (e.g. `blueshift-staging`, `blueshift-prod`),
   billing enabled on both.
2. Run `deploy/gcloud.sh` **once per project** (owner creds on that project):
   ```
   ENV_TIER=staging PROJECT=blueshift-staging GITHUB_REPO=<owner>/blueshift ./deploy/gcloud.sh
   ENV_TIER=prod    PROJECT=blueshift-prod    GITHUB_REPO=<owner>/blueshift ./deploy/gcloud.sh
   ```
   Optionally pass `BILLING_ACCOUNT=<id> BUDGET_AMOUNT=<usd>` to script the budget
   alert; otherwise create it by hand (the script prints the exact manual step).
3. Complete the per-project manual steps the script prints: enable
   Email/Password in Identity Platform; fill the three secret values; set the
   **budget alert** and **Speech-to-Text / Vertex AI quota caps** (staging tight,
   prod pilot-sized); map the domain for `blueshift-app`.
4. Set GitHub **repo variables**:
   `GCP_PROJECT_STAGING`, `GCP_PROJECT_PROD`, `GCP_REGION`, `STAGING_URL`.
5. Set GitHub **repo secrets** (each printed by the matching `gcloud.sh` run):
   `GCP_WIF_PROVIDER_STAGING`, `GCP_WIF_PROVIDER_PROD`,
   `GCP_DEPLOY_SA_STAGING`, `GCP_DEPLOY_SA_PROD`.

The **staging** job no-ops while `GCP_PROJECT_STAGING` is empty; **promote**
no-ops until **both** `GCP_PROJECT_STAGING` and `GCP_PROJECT_PROD` are set
(promote reads the tested image from the staging registry). Both are therefore
safe to land before any project exists.
