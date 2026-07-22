# Blueshift Studio

Broadcast-grade web app that turns long-form TV interviews into approved, captioned social
clips. English-first product; Persian (`fa`) is the first supported content language, behind
a language abstraction (`/internal/lang`) so new languages are additive, never structural.

Stack: Go (Cloud Run service + Jobs, one image) · SvelteKit static embedded in the Go binary ·
PostgreSQL 18 (sqlc, pgvector, pg_trgm) · GCS · ffmpeg/libass. Full rules live in
[CLAUDE.md](CLAUDE.md) — that file is binding for every agent in this repo.

## How this repo is built — the operating model

A solo developer runs this project through an LLM-driven workflow with **three roles and
strict separation**:

- **Architect** (main Claude session) — the only role the human talks to. Writes specs, ADRs,
  and docs; orchestrates the other two; arbitrates disputes; authorizes commits and
  screenshot-baseline updates. **Never writes implementation code.**
- **Implementer** ([.claude/agents/implementer.md](.claude/agents/implementer.md)) — takes one
  task spec, writes code + tests together, self-fixes until `make check` is green, attaches
  screenshot evidence for UI work.
- **Reviewer** ([.claude/agents/reviewer.md](.claude/agents/reviewer.md)) — adversarial by
  charter; sees spec + diff + screenshots (never the Implementer's reasoning); returns exactly
  `APPROVE` or `REJECT` with numbered findings. Read-only.

The loop: **spec → implement → review → (≤3 reject cycles) → approve → commit**. The reviewed
party never manages its reviewer. The human sees plans before execution and demos at
milestone gates ([docs/SPEC-M0.md](docs/SPEC-M0.md) … [docs/SPEC-M6.md](docs/SPEC-M6.md));
task state lives in [tasks/queue.md](tasks/queue.md).

## Deterministic gates

Agent beliefs are not trusted; gates are:

- `make check` — the single truth: fmt, vet, lint, race tests, svelte-check, eslint, vitest,
  builds, **vendor-leak gate** (provider names never reach client-visible surfaces), **hex
  gate** (raw colors only in `tokens.css`).
- Claude Code hooks ([.claude/settings.json](.claude/settings.json)) — auto-format on every
  write; `git commit` is blocked unless `make check` passes.
- Real git hooks ([.githooks/pre-commit](.githooks/pre-commit)) — same gate, enabled by
  `make setup`.
- CI ([.github/workflows/pr.yml](.github/workflows/pr.yml)) — check + eval + e2e gate every
  merge; [deploy.yml](.github/workflows/deploy.yml) rolls out prod progressively on
  main (no-traffic → migrate → smoke → 10% → watch → 100%); manual rollback job.
- Visual truth — [design/](design/) is the design contract; screenshot baselines only change
  when the Architect says so.

## Getting started

```sh
make setup     # git hooks, toolchain checks, web deps
make check     # the single truth (green on the bare scaffold)
make dev       # local dev loop (Go API + Vite hot reload)
make demo      # seeded offline full stack (app + worker + Postgres + sample)
make demo-down # tear down whatever make demo/dev started here
make e2e       # Playwright suite against make demo
make eval      # golden AI-output evals    (lands in M1)
```

One-time cloud provisioning: [deploy/gcloud.sh](deploy/gcloud.sh) (idempotent, commented).

### Run it locally — `make demo`

`make demo` boots the entire stack offline with deterministic seeded data and blocks
in the foreground. Prerequisites: ffmpeg, the `golang-migrate` CLI, `npm ci` in `web/`,
and Postgres — resolved in this order:

1. `DEMO_DATABASE_URL` if set and reachable (e.g. an existing local Postgres);
2. otherwise a Docker `pgvector/pgvector:pg18` container named `blueshift-demo-pg`
   (started and later removed by the demo — no other container is ever touched);
3. otherwise it fails with setup instructions.

It applies migrations, seeds the dev users and a fixed Persian sample episode (generated
with ffmpeg and run through the real worker ingest so it boots **READY**), then serves the
embedded UI at `http://127.0.0.1:8080`. Sign in with a seeded dev identity:

| Email | Password | Role |
|-------|----------|------|
| `dev-approver@blueshift.local` | `blueshift-dev` | approver |
| `dev-editor@blueshift.local`   | `blueshift-dev` | editor |

Stop with Ctrl-C, or `make demo-down` from another shell. `make dev` boots the same seeded
backend but serves the SPA via Vite with hot reload (proxying `/api` to the Go API), for
editing `web/src` live.

### UI verification — `make e2e`

`make e2e` runs the Playwright suite (`web/tests/`) against `make demo` (reused if already
up): the upload-to-Ready flow with keyboard paths, visual baselines at 1440×900 and
1280×800, token-conformance, RTL/ZWNJ, and an axe smoke. Visual baselines live in
`web/tests/__screenshots__/` and change only when the Architect authorises
`npx playwright test --update-snapshots`.

## Repo map

| Path | What |
|------|------|
| `cmd/app`, `cmd/worker`, `cmd/demoseed` | API server (embeds UI) · pipeline Job entrypoint · demo sample seeder |
| `internal/…` | api, ids, pipeline, asr, llm, media, lang (+`lang/fa`), store |
| `migrations/` | golang-migrate, **additive-only** |
| `web/` | SvelteKit; `components/ui` (vendored primitives) · `components/studio` (bespoke); `tests/` Playwright |
| `design/` | the design contract — single source of visual truth |
| `fixtures/`, `tools/eval/`, `tools/demo/` | gold data · offline eval scripts · make demo/dev orchestration (never deployed) |
| `docs/`, `tasks/` | specs, ADRs, demo script · task queue |
