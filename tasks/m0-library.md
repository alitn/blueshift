# Task: m0-library — Library page with live status, upload dialog, proxy playback

**Milestone:** M0 (docs/SPEC-M0.md §8) · **Type:** full-stack UI · **Slug:** `m0-library`

## Goal

The Library is real: an editor uploads a master through the UI, watches the pipeline status
move without refreshing, and plays the proxy when the episode is `Ready`.

## Design references (canonical)

Prototype screens **03 Library** and **05 First-run** + `design/DESIGN.md` (table row spec,
pipeline step bars, filter chips, status bar, empty state). PNGs still pending; prototype HTML
is ground truth.

## Architect rulings

- **Live status = polling.** `GET /api/episodes` every 3 s while any episode is in a
  non-terminal state (`uploaded`/`processing`), paused when the tab is hidden; no SSE in M0
  (Occam). Encapsulate in a small store so SSE can replace it later without touching the page.
- **Playback in a dialog.** Clicking OPEN on a `Ready` row opens the vendored dialog with a
  native `<video>` playing the signed proxy URL. No episode route in M0.
- **M0 status → 5-bar pipeline mapping** (bars per DESIGN.md `step-done/accent/border-default/danger`):
  `uploaded` = 1 done + label `QUEUED`; `processing` = 1 done, 2nd active + `INGEST…`;
  `ready` = all 5 done + `READY` (ok); `failed` = 2nd danger + `FAILED — INGEST` (danger).
  CLIPS/COST columns render `—` (no data until M1).

## Scope

1. **API (`internal/api`):**
   - `GET /api/episodes` — org-scoped list (excludes soft-deleted), DTO: `id` (`ep_…`),
     `title`, `source_filename`, `status`, `duration_ms`, `size_bytes` (if recorded),
     `uploaded_at`; neutral fields only.
   - `GET /api/episodes/{id}/proxy` — 200 `{url, expires_at}` signed GET (blob seam; 404 if
     not ready). Auth required; org-scoped.
   - `POST /api/episodes/{id}/retry` — allowed only from `failed`; resets to `uploaded`,
     fires the trigger; 409 otherwise.
2. **UI (`components/studio/` + route `/`):** Library table per screen 03 (columns
   ID | EPISODE | UPLOADED | DURATION | PIPELINE | CLIPS | COST | action), Persian titles
   `dir="rtl"` + `<bdi>` + `font-fa` inside LTR cells per DESIGN.md; filter chips
   `ALL n / PROCESSING n / READY n / FAILED n` (client-side); search input filtering
   title/filename/id (client-side); UPLOAD MASTER button (now enabled) → vendored dialog:
   file picker (mp4/mov/mxf, ≤40 GB), title field, progress bar during PUT (tokens only),
   then upload-complete → row appears and polls. Failed rows show danger RETRY (calls retry
   endpoint). Empty state (screen 05) when zero episodes. Keyboard: table rows focusable,
   Enter opens Ready row's player, `U` opens upload dialog (document in a hint chip per
   DESIGN.md keyboard-hints rule).
3. **Poll store** (`lib/`): interval + visibility handling + terminal-state stop; unit-tested
   with fake timers.
4. **Tests:** API handler tests (org isolation, proxy 404-before-ready, retry state guard);
   component tests: row rendering per status mapping (all four), RTL DOM assertions
   (`dir`, `<bdi>`, ZWNJ string preserved verbatim in rendered output — use a fixture title
   containing ZWNJ), filter/search logic, upload dialog flow with a mocked fetch, poll store.
   Playwright E2E + visual baselines + axe remain deferred to m0-demo-seed (Architect-recorded).
5. **Screenshots** to `.artifacts/screens/m0-library/`: library with seeded rows in all four
   states (drive the dev stack with local blob mode + a seeded DB if available; otherwise a
   storybook-less route-level mock — state honestly which), 1440×900 + 1280×800, plus the
   upload dialog open and the player dialog open.

## Out of scope

SSE, episode detail route, moments/clips columns with real data, render indicator wiring,
settings, delete/archive, pagination (M0 orgs are small).

## Acceptance

- `make check` fully green; vendor + hex gates green.
- With dev stack (auth dev mode, local blob, DB migrated+seeded, worker on PATH): UI flow
  upload → QUEUED → INGEST… → READY → play proxy works end-to-end without refresh; RETRY
  works from a failed row.
- RTL assertions pass; ZWNJ fixture title renders byte-identical.
- Screenshots match prototype screen 03 composition (Reviewer judges).

## Evidence to return

Summary + deviations; diffstat + status; tail of `make check`; screenshot paths; note on how
the four-state screenshots were produced; open questions.
