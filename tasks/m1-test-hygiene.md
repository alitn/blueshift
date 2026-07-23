# Task: m1-test-hygiene — DB-backed tests must not litter or depend on shared state

**Milestone:** M1 (gate reliability) · **Type:** backend tests · **Slug:** `m1-test-hygiene`

## Problem (bit the commit gate 2026-07-23, twice)

DB-backed Go tests run against the shared dev Postgres (TEST_DATABASE_URL → :5455) and
leave rows behind (193 episodes + 14 llm_calls accumulated in one day; purged
operationally twice). Consequences: `TestSweepAbandonedEpisodes` (exact `n == 1` assert
over a deliberately global sweep) fails on residue with an FK error via `llm_calls`,
blocking ANY commit whose gate runs with TEST_DATABASE_URL set; other tests are one
exact-count assert away from the same fate.

## Scope

1. **Isolation by default:** the store test harness creates (or migrates into) a
   dedicated scratch database per `go test` run — e.g. `blueshift_test_<pid/random>`
   created from the configured server URL, migrated in-process (the harness already
   self-migrates), dropped on success (kept on failure for debugging, name logged).
   Developers and the commit gate keep pointing TEST_DATABASE_URL at the server; tests
   never touch the named `blueshift` database again.
2. **Cleanup discipline:** shared fixtures use t.Cleanup to delete what they insert
   (belt) even though the scratch DB is dropped (suspenders).
3. **Residue-tolerant asserts:** exact global counts (`n == 1`) become gate-scoped
   asserts (per-row checks + `n >= expected`), matching the pattern
   `TestSweepStuckProcessingEpisodes` already uses.
4. **No behavior change to production code.** Test-only diff (plus optionally a tiny
   testutil package if the harness needs a home).
5. **CI parity:** pr.yml's postgres service flow must keep working unchanged (verify the
   scratch-DB creation works against the CI service container: the CI URL's role must
   have CREATEDB — adjust the CI role setup if needed, in pr.yml only).

## Acceptance

- make check green twice IN A ROW against a deliberately dirtied server (prove: insert
  junk rows into the named blueshift DB manually in your verification, run the suite,
  show green, show the junk untouched afterward).
- Reviewer verifies: no test writes to the named DB; scratch DB dropped on success;
  exact-count asserts eliminated or scoped; CI file change minimal.

## Evidence

Summary; diffs; the dirty-server verification transcript; open questions.
