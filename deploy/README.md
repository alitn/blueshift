# Deploy — configuration reference

One image (`Dockerfile`) ships both binaries. `deploy/gcloud.sh` provisions the
durable Google Cloud infrastructure once (idempotent); `.github/workflows/deploy.yml`
builds the image and creates/updates the Cloud Run service + worker Job on every
push to `main` (staging) and on manual promote (prod). Migrations run from the
deploy workflow through the Cloud SQL Auth Proxy against the repo `migrations/`
tree — there is no separate migrate binary or Job.

All env vars are read by `internal/config` (`config.Load`); the validation rules
there are the source of truth for what is required in which `ENV`.

## Secrets (Secret Manager → env var)

Created empty by `deploy/gcloud.sh`; values are filled by the human (see
`docs/RUNBOOK.md`). Never printed to client-visible surfaces.

| Secret id                  | Injected as      | Contents                                             |
| -------------------------- | ---------------- | ---------------------------------------------------- |
| `database-url`             | `DATABASE_URL`   | `postgres://app:…@/blueshift?host=/cloudsql/<inst>` (unix-socket DSN) |
| `session-signing-key`      | `SESSION_SECRET` | random 32B+; signs session cookies and upload tokens |
| `identity-platform-config` | `IDP_API_KEY`    | Identity Platform web API key (server-side sign-in)  |

## Service-account IAM (granted by `deploy/gcloud.sh`)

| SA                     | Roles                                                                                                                                              | Why                                                                          |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| `app-runtime@<project>`| `cloudsql.client`, `storage.objectAdmin`, `aiplatform.user`, `speech.client`, `secretmanager.secretAccessor`, `logging.logWriter`, `errorreporting.writer`, **`run.invoker`** | DB, blob, AI, secrets, logs — and `run.invoker` so the app can execute the worker Job (`jobs/{job}:run`) |
| `deployer@<project>`   | `run.admin`, `artifactregistry.writer`, `iam.serviceAccountUser`, `cloudsql.client`, **`errorreporting.viewer`**, + `secretmanager.secretAccessor` on **only** `database-url` | Build/deploy service+jobs, act as runtime SA, run `migrate up` via auth proxy, and read Error Reporting during the promote watch |

The worker-Job execute permission is project-scoped `run.invoker` rather than a
per-Job binding: the worker Jobs do not exist when `gcloud.sh` runs (deploy.yml
creates them), so a job-scoped binding could not be applied idempotently there.

## Cloud Run service — `blueshift-app` / `blueshift-app-staging`

ENTRYPOINT `/app/app`. Serves the embedded SPA + `/api`, `/healthz`, `/readyz`.

| Env var             | Value                              | Source (deploy.yml)          |
| ------------------- | ---------------------------------- | ---------------------------- |
| `ENV`               | `prod` / `staging`                 | `--set-env-vars`             |
| `AUTH_MODE`         | `identity`                         | `--set-env-vars`             |
| `WORKER_TRIGGER`    | `cloudrun`                         | `--set-env-vars`             |
| `WORKER_JOB_NAME`   | `blueshift-worker[-staging]`       | `--set-env-vars`             |
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

## Cloud Run Job — `blueshift-worker` / `blueshift-worker-staging`

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

## CI migration step (deploy.yml, staging + promote)

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

## GitHub repo configuration (set by the human after `gcloud.sh`)

`vars.GCP_PROJECT`, `vars.GCP_REGION`, `vars.STAGING_URL`,
`secrets.GCP_WIF_PROVIDER`, `secrets.GCP_DEPLOY_SA`. The deploy jobs no-op while
`vars.GCP_PROJECT` is empty.
