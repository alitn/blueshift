# Task: m0-upload — signed resumable upload to GCS, episode creation

**Milestone:** M0 (docs/SPEC-M0.md §6) · **Type:** backend/API · **Slug:** `m0-upload`

## Goal

An authenticated editor can create an episode and upload a master directly to GCS via a
narrowly-scoped signed resumable URL; the episode row exists with status `uploaded`. Fully
offline in dev/demo via a local blob store implementation.

## Architecture rulings (Architect — follow these)

- **Blob seam.** `internal/blob`: a minimal interface (`InitResumableUpload`, `SignedGetURL`,
  `Open/Write` as needed by the worker later) with two implementations: `gcs` (production —
  `cloud.google.com/go/storage`, declared stack, allowed dependency) and `localdir` (dev/demo:
  files under `BLOB_DIR`, upload via a local PUT endpoint the same handler serves; "signing"
  = short-lived HMAC query token reusing the session-secret machinery). This seam exists for
  the offline demo requirement; do not generalize it further.
- **Storage keys use public ids only** — internal bigints never leave the DB:
  `{org}/{episode}/masters/{sanitized-filename}` where `{org}`/`{episode}` are the
  `/internal/ids` encodings. Add an `org_` prefix to the ids registry (additive; exhaustive
  tests extended).
- **Config:** `BLOB_MODE=gcs|local` (default local in dev, required gcs in staging/prod),
  `GCS_BUCKET`, `BLOB_DIR`. GCS impl signs with the service-account credentials available to
  Cloud Run (no key files in repo).

## Scope

1. **`internal/blob`** per above, with unit tests for key building (rejects path traversal,
   weird filenames sanitized), local impl round-trip, HMAC token expiry/tamper; GCS impl
   compile-tested + thin (network paths exercised in staging, not unit tests).
2. **API (`internal/api`):**
   - `POST /api/episodes` (auth: any role) `{title, source_filename, size_bytes, content_type}`
     → validates (size cap 40 GB per design copy; content types mp4/mov/mxf), creates episode
     row (org-scoped, status `uploaded`, language default 'fa' from org config later — literal
     'fa' default is fine now per schema), builds master key, returns
     `{episode: {id: ep_…, …}, upload: {url, method, headers}}` — resumable session URL (gcs)
     or local PUT URL (local mode).
   - `POST /api/episodes/{id}/upload-complete` → verifies object exists (blob stat) and size
     matches, records `master_object_key`, keeps status `uploaded` (worker flips it in
     m0-worker-ingest). 409 on missing/short object.
   - DTOs neutral; episode ids rendered via `/internal/ids`; every query org-scoped from the
     session principal; sqlc queries added as needed (episodes update fields — additive).
3. **Tests:** handler tests with local blob mode end-to-end (create → PUT bytes → complete →
   row updated), authz (401 unauthenticated, cross-org isolation: principal of org A cannot
   touch org B episode — seed a second org in test fixtures, not migrations), validation
   failures, filename sanitization, ids round-trip of `org_`. Race-clean.

## Out of scope

Any UI (Library upload dialog lands in m0-library), worker/ffmpeg (m0-worker-ingest),
GCS bucket provisioning (deploy scripts, m0-ci-deploy), multi-file/chunk orchestration beyond
GCS resumable semantics, deletion/abort flows.

## Acceptance

- `make check` fully green; vendor gate green (`internal/api` DTOs neutral).
- Local-mode curl transcript: login (dev) → create episode → PUT a small fixture file to the
  returned URL → upload-complete → episodes row shows status `uploaded` + master key
  `org_…/ep_…/masters/…` (verify via a store query in a test or psql if available; otherwise
  the handler test covers it).
- Cross-org isolation test passes.

## Evidence to return

Summary + deviations; diffstat + status; tail of `make check`; the local-mode transcript;
open questions.
