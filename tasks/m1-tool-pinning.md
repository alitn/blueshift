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

## Approach (Go 1.24+ tool directive — no new dependency)

Go 1.26 is the toolchain. Use the `tool` directive so the CLI is version-locked in
go.mod/go.sum alongside the already-required library, reproducible for every dev and CI,
with no PATH dependency and no gitignored binary.

## Scope

1. **go.mod:** add the migrate CLI as a tool — `go get -tool
   github.com/golang-migrate/migrate/v4/cmd/migrate` — pinning it to the same module
   version already required (v4.19.1; if `go get -tool` bumps the module, keep the bump
   minimal and note it — the library require and the tool must stay the same version).
   Commit the resulting go.mod/go.sum changes.
2. **Invoke via `go tool`:** replace the bare `migrate …` invocations with
   `go tool migrate …` in `Makefile` (`migrate-up`) and `tools/demo/lib.sh`
   (`demo_migrate_seed`) and `tools/demo/*.sh` if any others call it. Keep the `-path
   migrations -database "$URL" up` args identical.
3. **make setup:** drop the "TODO: brew install golang-migrate" note; setup no longer
   needs a global migrate (go tool builds/caches it on first use). If a warmup is wanted,
   `go build` of the tool is optional — keep setup minimal.
4. **CI (pr.yml + any workflow running migrations):** ensure the migrate step uses
   `go tool migrate` (or `make migrate-up`) — verify no workflow relies on a separately
   installed migrate binary; adjust minimally if so.
5. **Docs:** update docs/RUNBOOK.md / docs/ENVIRONMENTS.md wherever they tell a human to
   install or run `migrate` to reflect `go tool migrate` (Architect owns docs/ — the
   implementer proposes the wording in the report; the Architect applies doc edits).

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
