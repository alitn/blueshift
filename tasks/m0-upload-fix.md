# Task: m0-upload-fix — prod upload failures: signBlob grant, orphan rows, poll cadence

**Milestone:** M0 (AC1 blocker, human-found) · **Type:** infra + backend + web fix · **Slug:** `m0-upload-fix`

## Findings (live prod, human demo attempt)

1. `POST /api/episodes` returned `{"error":"unavailable"}`: GCS V4 signing on Cloud Run
   needs the IAM signBlob API and the runtime SA lacked `iam.serviceAccounts.signBlob` on
   itself (verbatim 403 in logs). **Architect applied the grant operationally**; it must be
   in `deploy/gcloud.sh` (SA-scoped: app-runtime gets roles/iam.serviceAccountTokenCreator
   ON app-runtime itself, not project-wide) + deploy/README.md IAM table row.
2. **Orphaned rows:** the create handler inserts the episode BEFORE InitResumableUpload;
   when signing failed, each click left a stuck `uploaded` row (5 in prod). Fix the flow:
   if InitResumableUpload fails after insert, delete the just-created row (best-effort,
   log on failure) so a failed create is invisible; return the 503 as today. Add a handler
   test (fake blob store that errors on init → assert no row remains).
3. **Poll cadence:** human observed /api/episodes firing ~every 1s; spec is 3s single
   timer. Investigate pollStore usage: likely multiple concurrent poll loops stacking
   (e.g. re-subscription on upload dialog open/close or per-upload starts a new loop
   without stopping the old). Fix to guarantee exactly one active timer at 3s regardless
   of how many times polling is (re)started; extend the fake-timer unit test to cover
   double-start.

## Out of scope

Reaper for stuck rows (M1 backlog), retry UX polish.

## Acceptance

- make check green; new tests cover the orphan-rollback and double-start-poll cases.
- gcloud.sh re-runnable (idempotent grant); README row added.
- After deploy, Architect verifies a real prod upload end-to-end.

## Evidence

Summary; diffs; test output; open questions.
