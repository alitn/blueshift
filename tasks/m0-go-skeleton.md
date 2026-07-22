# Task: m0-go-skeleton — Go app server skeleton

**Milestone:** M0 (docs/SPEC-M0.md §2) · **Type:** backend · **Slug:** `m0-go-skeleton`

## Goal

`cmd/app` becomes a real HTTP server: health endpoints, structured logging, env-driven config,
embedded web build placeholder, graceful shutdown. After this task, `make check` runs the full
Go arm (gofmt, vet, lint, race tests) and `go build ./...` produces the app binary.

## Scope

1. **Module.** `go.mod`, module name `blueshift`, Go 1.24. **Standard library only** — any new
   dependency needs an ADR and is out of scope here.
2. **Server (`cmd/app`).** HTTP server on `PORT` (default `8080`), `net/http` with
   `http.ServeMux`. Read/write/idle timeouts set explicitly.
3. **Endpoints.**
   - `GET /healthz` → `200 {"status":"ok"}` — liveness, always OK while the process runs.
   - `GET /readyz` → `200 {"status":"ready","checks":{}}` — readiness with a pluggable check
     registry (`map[string]func(ctx) error`); empty for now (DB check arrives in m0-db-baseline).
     Any failing check → `503` with the failing check names.
4. **Structured logging.** `log/slog` JSON to stdout via a small custom handler/replacer that
   emits Cloud Logging-compatible keys: `severity` (DEBUG/INFO/WARNING/ERROR), `message`,
   `time`. Request-logging middleware (method, path, status, duration_ms, no bodies) and
   panic-recovery middleware (500 + ERROR log) wrap all routes.
5. **Config.** `internal/config`: typed struct loaded from env (`PORT`, `ENV` = dev|staging|prod,
   `LOG_LEVEL`). Fail fast with a clear error on invalid values. Secret Manager values arrive
   as env vars injected by Cloud Run (`--set-secrets`) — no Secret Manager client code in this
   task.
6. **Embedded UI.** `internal/webembed`: `go:embed` of a committed placeholder `dist/`
   (minimal `index.html`, no styling claims — the real build lands in m0-web-skeleton and will
   replace the contents via the Makefile). Serving rules: files by path; unknown paths that
   don't start with `/api/`, `/healthz`, `/readyz` fall back to `index.html` (SPA); no
   directory listings.
7. **Graceful shutdown.** SIGTERM/SIGINT → `http.Server.Shutdown` with 10s timeout; in-flight
   requests drain; exit 0 on clean shutdown; log start/stop.
8. **Tests (same change):** handlers (healthz, readyz ok + failing check), SPA fallback +
   static serving from the embed, config parsing (defaults, invalid values), middleware
   (request log fields, panic recovery), shutdown smoke test. All race-clean.

## Out of scope

Database, auth, GCS, worker, any `/api/` routes, any web build tooling, Dockerfile, deploy
changes, new dependencies.

## Acceptance

- `make check` fully green (Go arm now active: gofmt, vet, golangci-lint, `go test ./... -race`,
  build).
- `go run ./cmd/app` then: `curl :8080/healthz` → 200 JSON; `curl :8080/readyz` → 200 JSON;
  `curl :8080/` → placeholder index.html; `curl :8080/nonexistent` → index.html (SPA fallback).
- SIGTERM exits 0 after drain.
- No provider names anywhere client-visible (vendor gate stays green).

## Evidence to return

Summary; deviations (if any) with reasons; `git diff --stat`; tail of `make check`; open questions.

## Notes

- Log lines must never include request bodies or secrets.
- Keep `cmd/app/main.go` thin: wiring only; logic in `internal/`.
