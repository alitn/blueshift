# Task: m0-store-org-noop — unknown org must be not-found, not an error

**Milestone:** M0 · **Type:** backend fix · **Slug:** `m0-store-org-noop`

## Finding (first full CI run, PR #1 run 29973175389)

`internal/store` DB-backed test failed against real Postgres:
`pipeline_test.go:95: MarkFailed cross-org returned error: store: resolve org: no rows in result set`.
The store's org resolution surfaces `pgx.ErrNoRows` as an error when the org public id does
not exist; the contract everywhere else (and in the fake-repo tests) is that unknown/foreign
org ⇒ not-found/no-op, never an error. Local runs never caught it because DB tests skip
without TEST_DATABASE_URL.

## Scope

1. In `internal/store`, wherever org (or other entity) resolution can return
   `pgx.ErrNoRows`, map it to the package's not-found semantic (`found=false` / no-op with
   nil error per each method's contract) instead of propagating the raw error. Audit ALL
   store methods that resolve by public id (episodes get/list/update, pipeline
   Claim/MarkReady/MarkFailed, auth context) for the same leak — fix uniformly.
2. Tighten the DB-backed tests to cover unknown-org AND unknown-episode paths for the
   pipeline methods explicitly (they exist for cross-org; extend where thin).
3. No behavior change for the found paths; no API changes.

## Acceptance

- `make check` green locally (unit + race).
- If you can run DB-backed tests locally do so (local Postgres on :5455, create a scratch DB
  — do NOT touch the blueshift demo DB; use TEST_DATABASE_URL against a new database name and
  drop it after); otherwise state honestly and CI proves it on the PR re-run.
- Reviewer audits every resolveOrg/ErrNoRows path in the package.

## Evidence

Summary; diff; test results incl. DB-backed if run; open questions.
