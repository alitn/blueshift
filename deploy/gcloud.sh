#!/usr/bin/env bash
# ==============================================================================
# Blueshift Studio — one-time Google Cloud provisioning (idempotent).
# Region: us-central1. Everything here is safe to re-run: each step checks
# for existence before creating. No Terraform by design (see CLAUDE.md /
# "Occam with teeth"); this script IS the infrastructure record.
#
# Prereqs: gcloud CLI authenticated as an owner of $PROJECT; billing enabled.
# Usage:   PROJECT=blueshift-pilot GITHUB_REPO=you/blueshift ./deploy/gcloud.sh
# ==============================================================================
set -euo pipefail

PROJECT="${PROJECT:?set PROJECT=<gcp-project-id>}"
REGION="${REGION:-us-central1}"
GITHUB_REPO="${GITHUB_REPO:?set GITHUB_REPO=<owner>/<repo> for WIF}"
BUCKET="gs://${PROJECT}-media"
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

# ---- GCS: single bucket, lifecycle per CLAUDE.md ----------------------------
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

# ---- Service accounts --------------------------------------------------------
ensure_sa() { # name display
  gcloud iam service-accounts describe "$1@$PROJECT.iam.gserviceaccount.com" >/dev/null 2>&1 || \
    gcloud iam service-accounts create "$1" --display-name "$2"
}
ensure_sa app-runtime "Blueshift app+worker runtime"
ensure_sa deployer    "Blueshift CI deployer (GitHub Actions via WIF)"

RUNTIME="app-runtime@$PROJECT.iam.gserviceaccount.com"
DEPLOYER="deployer@$PROJECT.iam.gserviceaccount.com"

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
# Deployer: push images, deploy Cloud Run, execute jobs.
grant "$DEPLOYER" roles/run.admin
grant "$DEPLOYER" roles/artifactregistry.writer
grant "$DEPLOYER" roles/iam.serviceAccountUser

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
ensure_secret database-url        # postgres://app:...@/blueshift?host=/cloudsql/...
ensure_secret session-signing-key
ensure_secret identity-platform-config

# ---- Identity Platform -------------------------------------------------------
# Enabled via API above; email/password provider + tenant config is a one-time
# console step (no clean CLI). Roles live in Postgres (memberships), not IdP.
echo "MANUAL STEP: enable Email/Password sign-in in Identity Platform console."

# ---- Cloud Run service + jobs (placeholders until first image exists) --------
# First real deploy comes from .github/workflows/deploy.yml. Create the worker
# and migrate jobs here once an image has been pushed:
#   gcloud run jobs create blueshift-worker --image <img> --region $REGION \
#     --service-account $RUNTIME --max-retries 2 --task-timeout 3600
#   gcloud run jobs create blueshift-migrate --image <img> --region $REGION \
#     --service-account $RUNTIME --command /app/migrate
# Staging variants: blueshift-app-staging / blueshift-worker-staging /
# blueshift-migrate-staging (same image, ENV=staging).

echo "----------------------------------------------------------------------"
echo "Provisioning complete for $PROJECT ($REGION)."
echo "GitHub repo settings needed:"
echo "  vars.GCP_PROJECT=$PROJECT  vars.GCP_REGION=$REGION  vars.STAGING_URL=<url>"
echo "  secrets.GCP_WIF_PROVIDER=$POOL/providers/github"
echo "  secrets.GCP_DEPLOY_SA=$DEPLOYER"
echo "Remaining manual steps: Identity Platform provider, secret values,"
echo "  domain mapping for blueshift-app."
