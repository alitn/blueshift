# SPEC-M0 — Walking skeleton (weeks 1–2)

**Goal:** the thinnest possible end-to-end slice of the real system, deployed on the real
infrastructure, guarded by every gate. After M0, every later milestone is "add behavior to a
living system", never "wire up plumbing".

**Demo at gate:** a video uploaded on **prod** shows `Ready` in the Library with a playable proxy.

## In scope

1. **Scaffold** per the operating model: repo layout, `CLAUDE.md`, agents, hooks, Makefile,
   CI, deploy script, design contract, this spec. *(done — this commit)*
2. **Go service skeleton** — `cmd/app`: HTTP server, `/healthz`, `/readyz`, structured logging,
   config from env/Secret Manager, `go:embed` of the web build, graceful shutdown.
3. **Web skeleton** — SvelteKit adapter-static, TS strict, `tokens.css` generated from
   `design/DESIGN.md`, Tailwind theme from tokens, vendored `components/ui/` primitives
   (dialog, drawer, toast, tabs, select, tooltip, dropdown), app shell + Library page.
4. **Database baseline** — migration 0001 (additive-only from here on): extensions
   (`pgvector`, `pg_trgm`); `orgs`, `shows`, `users`, `memberships`, `episodes` (with
   `language`, `status` + CHECK, `public_id uuidv7`), `llm_calls`, `correction_log`,
   config table (incl. `allow_self_approval=true`, platform presets rows); seeds: one org,
   one show, seeded users. sqlc wired. `/internal/ids` codec (`ep_`/`sh_`/… base32) with
   exhaustive round-trip tests.
5. **Auth** — Identity Platform sign-in; session middleware; role + org-scoping middleware on
   every route (deny-by-default); roles read from `memberships`.
6. **Upload → GCS** — signed resumable upload scoped to `{org_id}/{episode_id}/masters/…`;
   episode row created with status `uploaded`.
7. **Worker Job** — `cmd/worker <episode_public_id> <stage>`; stage `ingest`: ffmpeg extracts
   audio + renders a browser-playable proxy to `proxies/`; status transitions
   `uploaded → processing → ready | failed` with per-stage timeouts, retries=2.
8. **Library live status** — Library page lists episodes with live stage status (poll or SSE),
   playable proxy on `Ready`.
9. **Demo/seed system** — `make demo`: local Postgres + app + worker with the sample fixture
   episode seeded and AI/external calls mocked from recorded fixtures; deterministic; offline.
   `make dev` loop. Playwright + vitest harness wired; first visual baselines (1440×900,
   1280×800) captured and committed; axe smoke wired.
10. **CI + deploy** — `pr.yml` green-gates merges; `deploy.yml` rolls out prod progressively
    on main per §7 (PoC ruling 2026-07-22: single project, no staging — docs/ENVIRONMENTS.md);
    `deploy/gcloud.sh` run once by the human.

## Out of scope

Transcription, diarization, moments, editor, rendering, captions — all M1. Any admin UI. Any
publishing. Anything not needed to move one video through upload → proxy → Ready on prod.

## Acceptance criteria

1. A video uploaded on **prod** shows `Ready` with a playable proxy in the Library.
2. A deliberately failing test **blocks deploy** (demonstrated once: red PR cannot merge,
   red main cannot promote).
3. A deliberately drifted screenshot **blocks merge** (demonstrated once against baselines).
4. `make check` green locally and in CI; commit with red `make check` is impossible
   (hook-blocked) — demonstrated once.
5. `make demo` boots offline and a Playwright run drives upload-to-Ready against it using the
   fixture (worker mocked to instant).
6. Vendor-leak and hex gates demonstrably fire on a seeded violation, then pass clean.

## Task breakdown (Architect will spec each into tasks/)

| Slug | Task | Depends on |
|------|------|-----------|
| `m0-scaffold` | This scaffold | — |
| `m0-go-skeleton` | app server, health, config, embed | scaffold |
| `m0-db-baseline` | migration 0001 + sqlc + ids codec | scaffold |
| `m0-web-skeleton` | SvelteKit + tokens + ui primitives + shell | design export |
| `m0-auth` | Identity Platform + authz middleware | go-skeleton, db-baseline |
| `m0-upload` | signed upload → GCS + episode create | auth |
| `m0-worker-ingest` | worker Job: audio + proxy + status | db-baseline |
| `m0-library` | Library page, live status, proxy playback | web-skeleton, upload |
| `m0-demo-seed` | make demo/dev, fixtures, e2e harness, baselines | library, worker |
| `m0-ci-deploy` | CI green-gates + prod pipeline live | all |

Each task ships with its tests, passes `make check`, and goes through the
Implementer → Reviewer loop. UI tasks additionally satisfy the UI Definition of Done.
