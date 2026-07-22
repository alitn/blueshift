SHELL := /bin/bash
.PHONY: setup check e2e eval demo demo-down dev build vendor-gate hex-gate migrate-up dev-seed sqlc

# ------------------------------------------------------------------------------
# make check — the single truth. Nothing red can ever be committed.
# Go and web steps self-activate once go.mod / web/package.json exist; the
# vendor-leak and hex gates are live from day one.
#
# Web toolchain (ADR 0001): bun is the package manager AND the runtime for the
# build/type-check/lint/test tools. `bunx --bun` / `bun --bun run` force the bun
# runtime so the native rollup/esbuild optional deps ALWAYS match the runtime
# arch bun installed them for — this is portable and immune to a host node whose
# arch differs from bun's. Playwright is the one exception: it has a hard Node
# runtime dependency, so `make e2e` pins it to node via `bunx --bun=false`.
# ------------------------------------------------------------------------------
check: vendor-gate hex-gate
	@set -e; \
	if [ -f go.mod ]; then \
		echo "--> gofmt"; \
		fmt=$$(gofmt -l . | grep -v '^web/' || true); \
		if [ -n "$$fmt" ]; then echo "gofmt needed on:"; echo "$$fmt"; exit 1; fi; \
		echo "--> go vet"; go vet ./...; \
		if command -v golangci-lint >/dev/null; then echo "--> golangci-lint"; golangci-lint run ./...; \
		else echo "WARN: golangci-lint not installed (make setup)"; fi; \
		echo "--> go test -race"; go test ./... -race; \
	else \
		echo "skip: Go checks (no go.mod yet)"; \
	fi
	@set -e; \
	if [ -f web/package.json ]; then \
		echo "--> svelte-check"; cd web && bunx --bun svelte-check --fail-on-warnings && \
		echo "--> eslint" && bunx --bun eslint . && \
		echo "--> vitest" && bunx --bun vitest run; \
	else \
		echo "skip: web checks (no web/package.json yet)"; \
	fi
	@$(MAKE) build
	@echo "check: GREEN"

# Build order: web build → copy static output into the Go embed dir → go build.
# The embed dir is gitignored (only .gitkeep is tracked); we rebuild it here so
# `//go:embed all:dist` picks up the fresh SPA before compiling the binary.
build:
	@set -e; \
	if [ -f web/package.json ]; then \
		echo "--> web build"; (cd web && bun --bun run build); \
		echo "--> copy web build -> internal/webembed/dist"; \
		rm -rf internal/webembed/dist; \
		mkdir -p internal/webembed/dist; \
		cp -R web/build/. internal/webembed/dist/; \
		touch internal/webembed/dist/.gitkeep; \
	else echo "skip: web build"; fi
	@set -e; if [ -f go.mod ]; then echo "--> go build"; go build ./...; else echo "skip: go build"; fi

# ------------------------------------------------------------------------------
# Vendor-leak gate: provider/model names must never appear in client-visible
# surfaces: web/, /internal/api (DTOs), migrations (seed data). See CLAUDE.md
# "Vendor neutrality". Additions to the list are cheap; leaks are not.
# ------------------------------------------------------------------------------
FORBIDDEN := chirp|gemini|vertex|google|speech-to-text|anthropic|claude|elevenlabs|openai|whisper|deepgram|assemblyai
LEAK_DIRS := web internal/api migrations

vendor-gate:
	@echo "--> vendor-leak gate"
	@matches=$$(grep -rinIE '$(FORBIDDEN)' $(LEAK_DIRS) \
		--exclude-dir=node_modules --exclude-dir=.svelte-kit --exclude-dir=build \
		--exclude-dir=__screenshots__ --exclude=package-lock.json --exclude=bun.lock --exclude=.gitkeep \
		2>/dev/null || true); \
	if [ -n "$$matches" ]; then \
		echo "$$matches"; echo "FAIL: provider name leaked into client-visible surface"; exit 1; \
	fi

# ------------------------------------------------------------------------------
# Hex gate: raw hex colors may only exist in web/src/lib/tokens.css.
# Also catches Tailwind arbitrary-value color utilities like bg-[#0af].
# ------------------------------------------------------------------------------
hex-gate:
	@echo "--> hex gate"
	@matches=$$(grep -rinIE '#[0-9a-fA-F]{3,8}\b|\-\[#' web/src \
		--exclude-dir=node_modules --exclude-dir=.svelte-kit \
		2>/dev/null | grep -v 'src/lib/tokens.css' || true); \
	if [ -n "$$matches" ]; then \
		echo "$$matches"; echo "FAIL: raw hex color outside tokens.css"; exit 1; \
	fi

# ------------------------------------------------------------------------------
# One-time local setup: git hooks + toolchain deps.
# ------------------------------------------------------------------------------
setup:
	git config core.hooksPath .githooks
	chmod +x .githooks/* .claude/hooks/*.sh
	@command -v golangci-lint >/dev/null || echo "TODO: install golangci-lint (brew install golangci-lint)"
	@command -v migrate >/dev/null || echo "TODO: install golang-migrate (brew install golang-migrate)"
	@command -v sqlc >/dev/null || echo "TODO: install sqlc (brew install sqlc) — codegen only, not a runtime dep"
	@command -v ffmpeg >/dev/null || echo "TODO: install ffmpeg (brew install ffmpeg)"
	@command -v bun >/dev/null || { echo "TODO: install bun — the web package manager (brew install oven-sh/bun/bun)"; }
	@if [ -f web/package.json ]; then command -v bun >/dev/null && (cd web && bun install) || echo "skip: web deps (bun not installed)"; fi
	@echo "setup: done (git hooks -> .githooks)"

# Playwright E2E against the demo stack. Real target lands with M0 web scaffold.
e2e:
	@if [ -f web/package.json ] && [ -d web/tests ]; then \
		cd web && bunx --bun=false playwright test; \
	else \
		echo "skip: e2e (web scaffold not present yet — arrives in M0)"; \
	fi

# Offline golden evals: WER, diarization anchor stability, caption fidelity,
# ZWNJ idempotence, .ass byte-exactness. Real target lands with the pipeline.
eval:
	@if [ -f tools/eval/run.py ]; then \
		python3 tools/eval/run.py --fixtures fixtures/; \
	else \
		echo "skip: eval (tools/eval/run.py not present yet — arrives with M1 pipeline)"; \
	fi

# Boot the full stack locally with deterministic seeded data so agents (and
# humans) can drive every flow offline. Postgres is resolved in this order:
# DEMO_DATABASE_URL (if reachable) -> a docker pgvector container named
# blueshift-demo-pg -> a clear failure with setup instructions. A fixed 2s
# sample episode is generated with ffmpeg and run through the REAL worker ingest
# so it boots 'ready'. Blocks in the foreground; Ctrl-C or `make demo-down`
# tears down only what this run created. No AI in M0 — nothing is mocked.
demo:
	@bash tools/demo/up.sh

# Tear down whatever `make demo`/`make dev` started here (the app, the Vite dev
# server, and the demo Postgres container iff this demo created it). Never
# touches a container or process it did not start.
demo-down:
	@bash tools/demo/down.sh

# Local dev loop: the seeded Go API + the Vite dev server (hot reload) with
# Vite proxying /api -> the API port. Same Postgres resolution as `make demo`.
dev:
	@bash tools/demo/dev.sh

# ------------------------------------------------------------------------------
# Database: apply additive-only migrations (used by demo/CI). Requires the
# golang-migrate CLI (make setup notes it) and DATABASE_URL.
# ------------------------------------------------------------------------------
migrate-up:
	@if [ -z "$$DATABASE_URL" ]; then echo "migrate-up: DATABASE_URL is not set"; exit 1; fi
	@command -v migrate >/dev/null || { echo "migrate-up: golang-migrate CLI not installed (make setup)"; exit 1; }
	migrate -path migrations -database "$$DATABASE_URL" up

# Load dev/demo user identities into an already-migrated database. Dev-only:
# staging/prod provision users per docs/RUNBOOK.md, never via this file. Wiring
# into `make demo`/`make dev` lands in m0-demo-seed. Requires psql + DATABASE_URL.
dev-seed:
	@if [ -z "$$DATABASE_URL" ]; then echo "dev-seed: DATABASE_URL is not set"; exit 1; fi
	@command -v psql >/dev/null || { echo "dev-seed: psql not installed"; exit 1; }
	psql "$$DATABASE_URL" -v ON_ERROR_STOP=1 -f fixtures/dev-seed.sql

# Regenerate the sqlc query layer (internal/store/db). sqlc is a dev-only
# codegen tool; the generated code is committed and `make check` never needs it.
sqlc:
	@command -v sqlc >/dev/null || { echo "sqlc: not installed (brew install sqlc)"; exit 1; }
	sqlc generate
