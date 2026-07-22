# Task: m0-demo-seed — make demo/dev, fixtures, e2e harness, first baselines

**Milestone:** M0 (docs/SPEC-M0.md §9, AC5) · **Type:** tooling + UI QA harness · **Slug:** `m0-demo-seed`

## Goal

`make demo` boots the entire stack locally, offline, deterministic, with seeded data — and the
Playwright/vitest harness drives upload-to-Ready against it, capturing the repo's **first
committed visual baselines**.

## Architect rulings

- **Baseline authorization.** This task creates the initial screenshot baselines in
  `web/tests/__screenshots__/` (1440×900 and 1280×800). I authorize this one-time creation
  explicitly. Any later change to these files requires my authorization again.
- **Postgres for demo:** in order of preference: (1) use `DEMO_DATABASE_URL` if set and
  reachable; (2) `docker run` a `pgvector/pgvector:pg18`-family container (name
  `blueshift-demo-pg`, stopped/removed by `make demo-down`); (3) fail with clear setup
  instructions. Never touch any other running container or process (CLAUDE.md standing rule).
- **Deterministic sample:** the fixture episode is generated at demo boot with fixed ffmpeg
  args (`testsrc2` + sine, 2 s) into `BLOB_DIR`, then run through the REAL worker ingest to
  produce proxy/audio; the episode row (Persian title containing ZWNJ, e.g.
  `گفت‌وگوی نمونه`, status ready) is inserted via a fixture SQL/Go seeder. No AI calls exist
  yet in M0 — nothing to mock; the recorded-fixture policy activates in M1.
- **Allowed new dev-deps:** `@playwright/test`, `@axe-core/playwright`. Nothing else.

## Scope

1. **`make demo`:** migrations up → `fixtures/dev-seed.sql` → sample episode fixture → app
   (`AUTH_MODE=dev`, `BLOB_MODE=local`, `WORKER_TRIGGER=exec`, fixed dev `SESSION_SECRET`) on
   `PORT` (default 8080); prints sign-in instructions (dev users). `make demo-down` tears
   down (only what demo created). Fully offline after `npm ci`/images present.
2. **`make dev`:** app (dev env) + `cd web && npm run dev` with a Vite proxy for `/api` →
   app port; documented in README/Makefile comments.
3. **Playwright harness (`web/tests/`):** playwright config with two viewport projects
   (1440×900, 1280×800), `webServer` that boots `make demo` (reuse if already up); specs:
   - **Flow (AC5):** login (dev approver) → Library shows sample episode `READY` → upload a
     tiny generated fixture via the UI dialog → observe `QUEUED`/`INGEST…` → `READY` without
     reload (poll) → open player, assert playing proxy `<video>` src. Keyboard paths: `U`
     opens upload, `Enter` on focused Ready row opens player, Escape closes.
   - **Visual baselines:** `toHaveScreenshot` of Library (seeded state) and login at both
     viewports; baselines committed.
   - **Token conformance:** computed styles of top bar, status bar, primary button, card
     match the corresponding `tokens.css` custom-property values read at runtime via
     `getComputedStyle` — **no hex literals in test code** (hex gate applies to web/tests
     too since it greps web/src only — still, keep tests token-derived).
   - **RTL:** sample episode title renders `dir="rtl"` within `<bdi>`, ZWNJ (‌)
     preserved byte-exact in `textContent`.
   - **axe smoke:** `/` and `/login`, no new critical violations.
4. **`make e2e`:** runs the Playwright suite (webServer handles the stack); wired so
   `pr.yml`'s existing `make e2e` step works once CI has ffmpeg + Postgres (that CI wiring is
   m0-ci-deploy's job — note the handoff, don't touch workflows here).
5. **Docs:** README quickstart section (setup → make demo → sign in) — brief.

## Out of scope

CI workflow changes (m0-ci-deploy), Dockerfile, any new UI features, AI mocks (M1),
performance tuning.

## Acceptance

- On a clean checkout with deps: `make demo` boots offline; the full Playwright suite passes
  locally; baselines exist at both viewports and are committed; axe smoke green; `make check`
  fully green; `make demo-down` leaves no stray processes/containers other than what existed
  before.
- Deliberately breaking a token (locally, reverted) makes the token-conformance test fail —
  include the red/green proof output in evidence.

## Evidence to return

Summary + deviations; diffstat + status; tail of `make check` and of the Playwright run;
baseline file list; the token-conformance red/green proof; screenshots of the seeded Library
to `.artifacts/screens/m0-demo-seed/`; open questions.
