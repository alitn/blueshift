# Task: m1-tool-pinning — pin the migrate CLI at the project level (Go tool directive)

**Milestone:** M1 (reproducibility; human-directed 2026-07-23) · **Type:** build/tooling · **Slug:** `m1-tool-pinning`

## Problem

The `migrate` CLI (golang-migrate) is not pinned at the project level. `go.mod` pins the
*library* `github.com/golang-migrate/migrate/v4 v4.19.1`, but the CLI a dev/CI runs is
"whatever is on PATH" — brew, a stray `go install`, or the gitignored `.demo/bin/migrate`.
`make setup` only prints a TODO to install it; `Makefile:migrate-up` and
`tools/demo/lib.sh` do `command -v migrate` and error if absent. Consequences: library
and CLI can silently drift, every machine can run a different `migrate`, and PATH
ambiguity is the exact class of problem that produced a segfaulting decade-old Intel
`migrate` on the human's machine.

## Approach (REVISED 2026-07-23 — `go run -tags postgres`, not the `tool` directive)

The `go tool` directive CANNOT work for golang-migrate v4.19.1: every DB/source driver is
behind a build tag (`//go:build postgres` etc.), `go tool` provides no way to pass build
tags, and `go get -tool …/cmd/migrate` explodes go.sum 180→866 lines (~90 unwanted DB
drivers) — contradicting "no new dependency". Verified empirically by the Implementer.

**Approved mechanism:** `go run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate
-path migrations -database "$URL" up`. It stays version-locked to the existing
`require github.com/golang-migrate/migrate/v4 v4.19.1` (tool == library, cannot drift),
`go run` accepts `-tags` so the pq driver compiles in, args are unchanged, and it adds
ZERO new deps (`github.com/lib/pq` is already in go.sum; `internal/dbtest` already
blank-imports migrate drivers). No PATH binary, reproducible per dev/CI.

## Scope (revised)

1. **go.mod/go.sum:** NO change (no `tool` directive). Keep the existing
   `require github.com/golang-migrate/migrate/v4 v4.19.1`. `go run` resolves the CLI from
   that require.
2. **Makefile `migrate-up`:** drop the `command -v migrate` guard; run
   `go run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate -path
   migrations -database "$$DATABASE_URL" up` (a `MIGRATE :=` var + a one-line comment
   noting the `postgres` tag registers the pq driver). Update the nearby comment block.
3. **tools/demo/lib.sh `demo_migrate_seed`:** drop the `command -v migrate … die` guard;
   invoke via `( cd "$REPO_ROOT" && go run -tags 'postgres' …/cmd/migrate -path migrations
   -database "$DB_URL" up )` (explicit cd so `go run` resolves the module + relative
   `migrations`). Keep args identical. up.sh/dev.sh need no change (verify).
4. **make setup:** delete the "TODO: brew install golang-migrate" line; keep setup minimal
   (no mandatory prebuild).
5. **CI:** delete the "Install golang-migrate" steps in pr.yml and baselines.yml (both
   already have setup-go; make demo → demo_migrate_seed now uses go run).
6. **deploy.yml (prod migration step): LEAVE AS-IS** — it already downloads a pinned
   v4.19.1 release binary that matches the library require and runs in an isolated CI
   runner (no PATH drift). Converting the sensitive prod deploy path is a deliberate
   non-goal here (Architect decision 2026-07-23: don't touch the working prod migration
   path in a tooling task); note the deliberate asymmetry in a Makefile/CI comment.
7. **Docs:** RUNBOOK/ENVIRONMENTS have no migrate install/run instructions to change
   (verified); Architect will add a one-line note to docs/DEMO.md if wanted.

## Out of scope

Pinning sqlc or other tools (follow-up if this pattern is adopted); removing the
gitignored `.demo/bin/migrate` (harmless; the demo scripts stop depending on it).

## Acceptance

- make check green; `make migrate-up` and the demo migrate path work via `go tool migrate`
  against a scratch/dev DB (prove it in the report).
- Reviewer verifies: tool + library versions match in go.mod; no bare `migrate` invocation
  remains in Makefile/tools/CI; go.sum consistent; migrations still apply cleanly.

## Evidence

Summary; diffs; a `go tool migrate -version`/up transcript; open questions.
