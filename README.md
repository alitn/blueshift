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
  merge; [deploy.yml](.github/workflows/deploy.yml) ships staging automatically and prod by
  gradual, watched promotion.
- Visual truth — [design/](design/) is the design contract; screenshot baselines only change
  when the Architect says so.

## Getting started

```sh
make setup     # git hooks, toolchain checks, web deps
make check     # the single truth (green on the bare scaffold)
make dev       # local dev loop            (lands in M0)
make demo      # seeded offline full stack (lands in M0)
make e2e       # Playwright                (lands in M0)
make eval      # golden AI-output evals    (lands in M1)
```

One-time cloud provisioning: [deploy/gcloud.sh](deploy/gcloud.sh) (idempotent, commented).

## Repo map

| Path | What |
|------|------|
| `cmd/app`, `cmd/worker` | API server (embeds UI) · pipeline Job entrypoint |
| `internal/…` | api, ids, pipeline, asr, llm, media, lang (+`lang/fa`), store |
| `migrations/` | golang-migrate, **additive-only** |
| `web/` | SvelteKit; `components/ui` (vendored primitives) · `components/studio` (bespoke) |
| `design/` | the design contract — single source of visual truth |
| `fixtures/`, `tools/eval/` | gold data · offline eval scripts (never deployed) |
| `docs/`, `tasks/` | specs, ADRs, demo script · task queue |
