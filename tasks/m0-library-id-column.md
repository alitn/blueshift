# Task: m0-library-id-column — remove the raw-id column from the Library table

**Milestone:** M0 (human ruling) · **Type:** UI tweak · **Slug:** `m0-library-id-column`

## Ruling (recorded in design/DESIGN.md)

Raw public ids (`ep_…`) are never displayed in the UI; the prototype's ID column shows
editorial episode codes which don't exist yet as data. Until they do (M1+), the Library
table has **no ID column**.

## Scope

1. Remove the ID column from the Library table (header + cell) in
   `web/src/lib/components/studio/LibraryTable.svelte`; EPISODE becomes the first column;
   keep all other columns, widths rebalanced sensibly within the existing token/spacing
   system (no new values).
2. Search continues to match the id (`ep_…`) as a hidden field if it currently does —
   users may paste an id from a URL — but nothing renders it.
3. Update affected component tests and any test that asserted the ID cell.
4. Playwright specs: check `web/tests/` for selectors/assertions touching the ID column and
   update them (baselines don't exist yet, so no baseline churn).
5. Screenshot evidence to `.artifacts/screens/m0-library-id-column/` (1440×900) using the
   same isolated-browser method as before (never touch the human's running browser; kill
   only PIDs you spawn).

## Out of scope

Episode codes (M1), API changes (the DTO keeps `id` — clients need it for routing), any
other column changes.

## Acceptance

- `make check` fully green; component tests updated and passing.
- Screenshot shows the table without an ID column, EPISODE first, layout balanced.

## Evidence to return

Summary; diffstat; tail of `make check`; screenshot path; open questions.
