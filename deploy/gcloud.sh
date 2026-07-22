#!/usr/bin/env bash
# ==============================================================================
# Blueshift Studio — one-time Google Cloud provisioning (idempotent).
# Region: us-central1. Everything here is safe to re-run: each step checks
# for existence before creating. No Terraform by design (see CLAUDE.md /
# "Occam with teeth"); this script IS the infrastructure record.
#
# PoC scope: ONE GCP project hosts prod (see docs/ENVIRONMENTS.md). Run this
# script ONCE for that project. It provisions the prod runtime + CI deployer,
# the prod media bucket, Cloud SQL, secrets, and WIF — AND a separate
# dev-experiments@ SA + <project>-media-dev bucket used only for local ASR/LLM
# fixture capture (never Cloud SQL, never the prod bucket, never Cloud Run).
# The Cloud Run service + worker Job are blueshift-app / blueshift-worker.
#
# Prereqs: gcloud CLI authenticated as an owner of $PROJECT; billing enabled.
# Usage (run once):
#   PROJECT=blueshift-prod GITHUB_REPO=you/blueshift ./deploy/gcloud.sh
# Optional (cost guardrails, see "Cost & quota guardrails" below):
#   BILLING_ACCOUNT=XXXXXX-XXXXXX-XXXXXX BUDGET_AMOUNT=50
# ==============================================================================
set -euo pipefail

PROJECT="${PROJECT:?set PROJECT=<gcp-project-id>}"
REGION="${REGION:-us-central1}"
GITHUB_REPO="${GITHUB_REPO:?set GITHUB_REPO=<owner>/<repo> for WIF}"
BILLING_ACCOUNT="${BILLING_ACCOUNT:-}"  # optional: enables scripted budget-alert creation
BUDGET_AMOUNT="${BUDGET_AMOUNT:-50}"    # monthly budget alert threshold, USD
BUCKET="gs://${PROJECT}-media"
DEV_BUCKET="gs://${PROJECT}-media-dev"
SQL_INSTANCE="blueshift-pg"

gcloud config set project "$PROJECT" >/dev/null

# ---- APIs --------------------------------------------------------------------
# (enable is inherently idempotent)
gcloud services enable \
  run.googleapis.com sqladmin.googleapis.com storage.googleapis.com \
  artifactregistry.googleapis.com secretmanager.googleapis.com \
  aiplatform.googleapis.com speech.googleapis.com \
  identitytoolkit.googleapis.com iamcredentials.googleapis.com \
  logging.googleapis.com clouderrorreporting.googleapis.com

# ---- Artifact Registry -------------------------------------------------------
if ! gcloud artifacts repositories describe blueshift --location "$REGION" >/dev/null 2>&1; then
  gcloud artifacts repositories create blueshift \
    --repository-format=docker --location "$REGION" \
    --description "Blueshift app+worker image"
fi

# ---- Cloud SQL: PostgreSQL 18, smallest tier, no HA (pilot), backups + PITR --
if ! gcloud sql instances describe "$SQL_INSTANCE" >/dev/null 2>&1; then
  gcloud sql instances create "$SQL_INSTANCE" \
    --database-version=POSTGRES_18 --region "$REGION" \
    --tier=db-g1-small --availability-type=ZONAL \
    --backup --enable-point-in-time-recovery \
    --database-flags=cloudsql.iam_authentication=on
fi
gcloud sql databases describe blueshift --instance "$SQL_INSTANCE" >/dev/null 2>&1 || \
  gcloud sql databases create blueshift --instance "$SQL_INSTANCE"
gcloud sql users list --instance "$SQL_INSTANCE" --format 'value(name)' | grep -qx app || \
  gcloud sql users create app --instance "$SQL_INSTANCE" \
    --password "$(openssl rand -base64 24)" # rotate into Secret Manager below
# Extensions (pgvector, pg_trgm) are created by migration 0001, not here.

# ---- GCS: single prod bucket, lifecycle per CLAUDE.md ------------------------
if ! gcloud storage buckets describe "$BUCKET" >/dev/null 2>&1; then
  gcloud storage buckets create "$BUCKET" --location "$REGION" \
    --uniform-bucket-level-access --public-access-prevention
fi
# masters/ -> Nearline at 30d, delete at 90d (unless flagged; flagged objects
# get a "keep" metadata tag and a matchesPrefix carve-out handled in app code).
cat > /tmp/blueshift-lifecycle.json <<'EOF'
{
  "rule": [
    { "action": {"type": "SetStorageClass", "storageClass": "NEARLINE"},
      "condition": {"age": 30, "matchesPrefix": ["masters/"]} },
    { "action": {"type": "Delete"},
      "condition": {"age": 90, "matchesPrefix": ["masters/"]} }
  ]
}
EOF
gcloud storage buckets update "$BUCKET" --lifecycle-file=/tmp/blueshift-lifecycle.json

# ---- GCS: dev-experiments scratch bucket (local fixture capture only) --------
# The dev-experiments@ SA (below) uploads extracted audio here to capture
# ASR/LLM fixtures from a laptop, then deletes the temp object. It is throwaway —
# a short lifecycle deletes anything left behind. It is NEVER the prod bucket and
# holds no customer data. See docs/ENVIRONMENTS.md "Live-provider usage".
if ! gcloud storage buckets describe "$DEV_BUCKET" >/dev/null 2>&1; then
  gcloud storage buckets create "$DEV_BUCKET" --location "$REGION" \
    --uniform-bucket-level-access --public-access-prevention
fi
cat > /tmp/blueshift-dev-lifecycle.json <<'EOF'
{
  "rule": [
    { "action": {"type": "Delete"}, "condition": {"age": 7} }
  ]
}
EOF
gcloud storage buckets update "$DEV_BUCKET" --lifecycle-file=/tmp/blueshift-dev-lifecycle.json

# ---- Service accounts --------------------------------------------------------
ensure_sa() { # name display
  gcloud iam service-accounts describe "$1@$PROJECT.iam.gserviceaccount.com" >/dev/null 2>&1 || \
    gcloud iam service-accounts create "$1" --display-name "$2"
}
ensure_sa app-runtime     "Blueshift app+worker runtime"
ensure_sa deployer        "Blueshift CI deployer (GitHub Actions via WIF)"
ensure_sa dev-experiments "Blueshift local ASR/LLM fixture capture (dev only)"

RUNTIME="app-runtime@$PROJECT.iam.gserviceaccount.com"
DEPLOYER="deployer@$PROJECT.iam.gserviceaccount.com"
DEV_SA="dev-experiments@$PROJECT.iam.gserviceaccount.com"

grant() { gcloud projects add-iam-policy-binding "$PROJECT" \
  --member "serviceAccount:$1" --role "$2" --condition=None >/dev/null; }
# Runtime: DB, storage, AI, secrets, logs. (add-iam-policy-binding is idempotent)
grant "$RUNTIME" roles/cloudsql.client
grant "$RUNTIME" roles/storage.objectAdmin
grant "$RUNTIME" roles/aiplatform.user
grant "$RUNTIME" roles/speech.client
grant "$RUNTIME" roles/secretmanager.secretAccessor
grant "$RUNTIME" roles/logging.logWriter
grant "$RUNTIME" roles/errorreporting.writer
# The app executes the worker Cloud Run Job (POST jobs/{job}:run, see
# internal/pipeline/trigger.go), which needs run.jobs.run. roles/run.invoker
# grants it. A job-scoped binding would be tighter but the worker Jobs do not
# exist until deploy.yml's first run, so this project-level grant is the
# idempotent choice runnable before any image/job exists.
grant "$RUNTIME" roles/run.invoker
# Deployer: push images, deploy Cloud Run service+jobs, act as the runtime SA,
# run additive migrations from CI through the Cloud SQL Auth Proxy, and read
# Error Reporting during the rollout watch.
grant "$DEPLOYER" roles/run.admin
grant "$DEPLOYER" roles/artifactregistry.writer
grant "$DEPLOYER" roles/iam.serviceAccountUser
grant "$DEPLOYER" roles/cloudsql.client        # auth-proxy connection for `migrate up`
grant "$DEPLOYER" roles/errorreporting.viewer  # rollout-watch error-event query

# ---- dev-experiments: local fixture capture ONLY -----------------------------
# It may invoke Speech-to-Text and Vertex AI (project-scoped invocation roles)
# and read/write ONLY the dev scratch bucket (bucket-scoped IAM, applied AFTER
# the bucket exists). It gets NO Cloud SQL, NO Cloud Run, and NO access to the
# prod bucket — the bucket-scoped binding cannot reach any other bucket, and no
# project-level storage role is granted. It has no WIF binding: it is never used
# by CI, only by developers via short-lived local ADC impersonation (below).
grant "$DEV_SA" roles/aiplatform.user   # Vertex AI invocation
grant "$DEV_SA" roles/speech.client     # Speech-to-Text invocation
gcloud storage buckets add-iam-policy-binding "$DEV_BUCKET" \
  --member "serviceAccount:$DEV_SA" \
  --role roles/storage.objectAdmin >/dev/null

# ---- Workload Identity Federation for GitHub Actions ------------------------
if ! gcloud iam workload-identity-pools describe github --location global >/dev/null 2>&1; then
  gcloud iam workload-identity-pools create github --location global \
    --display-name "GitHub Actions"
fi
if ! gcloud iam workload-identity-pools providers describe github \
     --location global --workload-identity-pool github >/dev/null 2>&1; then
  gcloud iam workload-identity-pools providers create-oidc github \
    --location global --workload-identity-pool github \
    --issuer-uri "https://token.actions.githubusercontent.com" \
    --attribute-mapping "google.subject=assertion.sub,attribute.repository=assertion.repository" \
    --attribute-condition "assertion.repository == '$GITHUB_REPO'"
fi
POOL=$(gcloud iam workload-identity-pools describe github --location global --format 'value(name)')
gcloud iam service-accounts add-iam-policy-binding "$DEPLOYER" \
  --role roles/iam.workloadIdentityUser \
  --member "principalSet://iam.googleapis.com/$POOL/attribute.repository/$GITHUB_REPO" >/dev/null

# ---- Secrets (create empty holders; fill values manually) --------------------
ensure_secret() {
  gcloud secrets describe "$1" >/dev/null 2>&1 || \
    gcloud secrets create "$1" --replication-policy automatic
}
ensure_secret database-url        # postgres://app:...@/blueshift?host=/cloudsql/<instance>
ensure_secret session-signing-key # -> SESSION_SECRET
ensure_secret identity-platform-config # -> IDP_API_KEY (Identity Platform web API key)

# The CI deployer reads the database-url DSN to run migrations via the auth
# proxy (deploy.yml). Runtime already has project-level secretAccessor; grant
# the deployer read on just this one secret.
gcloud secrets add-iam-policy-binding database-url \
  --member "serviceAccount:$DEPLOYER" \
  --role roles/secretmanager.secretAccessor >/dev/null

# ---- Identity Platform -------------------------------------------------------
# Enabled via API above; email/password provider + tenant config is a one-time
# console step (no clean CLI). Roles live in Postgres (memberships), not IdP.
echo "MANUAL STEP: enable Email/Password sign-in in Identity Platform console."

# ---- Cost & quota guardrails -------------------------------------------------
# The project runs LIVE ASR/LLM (Speech-to-Text, Vertex AI) — in prod, and from
# the dev-experiments@ SA during fixture capture — so it needs a budget alert
# and explicit quota caps. Default local development stays on recorded fixtures
# (offline, free); see docs/ENVIRONMENTS.md and CLAUDE.md.
#
# Budget alert: created idempotently by displayName IF a billing account is
# supplied; the Budgets API has no create-if-absent, so we list-then-create.
if [ -n "$BILLING_ACCOUNT" ] && gcloud billing budgets list \
     --billing-account "$BILLING_ACCOUNT" >/dev/null 2>&1; then
  BUDGET_NAME="blueshift-$PROJECT"
  if gcloud billing budgets list --billing-account "$BILLING_ACCOUNT" \
       --format 'value(displayName)' 2>/dev/null | grep -qx "$BUDGET_NAME"; then
    echo "budget alert '$BUDGET_NAME' already exists — skipping."
  else
    gcloud billing budgets create \
      --billing-account "$BILLING_ACCOUNT" \
      --display-name "$BUDGET_NAME" \
      --filter-projects "projects/$PROJECT" \
      --budget-amount "${BUDGET_AMOUNT}USD" \
      --threshold-rule=percent=0.5 --threshold-rule=percent=0.9 --threshold-rule=percent=1.0
  fi
else
  echo "MANUAL STEP: create a monthly budget alert for $PROJECT (no BILLING_ACCOUNT given,"
  echo "  or the billing CLI is unavailable). Console: Billing > Budgets & alerts >"
  echo "  Create budget, scope to project '$PROJECT', amount ~\$${BUDGET_AMOUNT},"
  echo "  thresholds 50/90/100%. Or re-run with BILLING_ACCOUNT=<id> BUDGET_AMOUNT=<usd>."
fi
# Quota caps: Service Usage consumer quota overrides for Speech-to-Text and
# Vertex AI. These are per-metric QuotaPreferences with no stable idempotent
# gcloud verb across versions, so they are a documented manual step:
echo "MANUAL STEP: cap live-AI quotas for $PROJECT (Console: IAM & Admin > Quotas,"
echo "  or 'gcloud alpha services quota update'):"
echo "  - Speech-to-Text: cap per-minute / per-day request quota."
echo "  - Vertex AI (aiplatform): cap online-prediction / generate-content request quota."
echo "  Size to the pilot; keep headroom low so a runaway job is bounded."

# ---- Local ADC for dev-experiments (fixture capture) -------------------------
# Developers never hold prod credentials. To capture fixtures locally, mint
# short-lived ADC by IMPERSONATING dev-experiments@ (no downloaded JSON key).
# The owner grants a developer impersonation once, then each session mints ADC:
echo "INFO: local fixture capture uses dev-SA impersonation (no JSON keys):"
echo "  one-time (owner grants a developer impersonation on the dev SA):"
echo "    gcloud iam service-accounts add-iam-policy-binding $DEV_SA \\"
echo "      --member=user:<you@example.com> --role=roles/iam.serviceAccountTokenCreator"
echo "  per session (mint local ADC that impersonates the dev SA):"
echo "    gcloud auth application-default login --impersonate-service-account=$DEV_SA"
echo "  the local worker then uploads audio to $DEV_BUCKET, calls the AI APIs,"
echo "  stores the transcript in LOCAL Postgres, and deletes the temp object."

# ---- Cloud Run service + jobs (created by CI, not here) ----------------------
# There is no image until the first deploy, so the service and job are created
# and kept in sync by .github/workflows/deploy.yml (create-or-update semantics):
#   blueshift-app     (Cloud Run service)
#   blueshift-worker  (Cloud Run Job)
# Both run the SAME image; the worker Job overrides ENTRYPOINT with
#   --command /app/worker  (+ <episode> <stage> args supplied per execution).
# On push to main, deploy.yml builds the image and rolls it out through the
# progressive rollout (--no-traffic candidate -> migrate -> smoke -> 10% ->
# watch -> 100%); there is no separate promote and no cross-project image copy.
# Env/secret wiring per service is in deploy/README.md and must match the secret
# ids created above (database-url / session-signing-key / identity-platform-config)
# and $RUNTIME as the service account.
#
# Migrations: there is NO separate migrate binary or migrate Job. deploy.yml
# runs `migrate up` from the CI runner against Cloud SQL through the Cloud SQL
# Auth Proxy, using the migrations/ tree in the repo — the one migration source
# also used by `make demo` and the DB-backed tests.

echo "----------------------------------------------------------------------"
echo "Provisioning complete for $PROJECT ($REGION)."
echo "GitHub repo settings needed (4):"
echo "  vars.GCP_PROJECT=$PROJECT"
echo "  vars.GCP_REGION=$REGION"
echo "  secrets.GCP_WIF_PROVIDER=$POOL/providers/github"
echo "  secrets.GCP_DEPLOY_SA=$DEPLOYER"
echo "Remaining manual steps: Identity Platform provider, secret values,"
echo "  budget alert + AI quota caps (above), domain mapping for blueshift-app,"
echo "  and (optional) grant a developer impersonation on $DEV_SA for local"
echo "  fixture capture."
