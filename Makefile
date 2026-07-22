SHELL := /bin/bash
.PHONY: setup check e2e eval demo dev build vendor-gate hex-gate

# ------------------------------------------------------------------------------
# make check — the single truth. Nothing red can ever be committed.
# Go and web steps self-activate once go.mod / web/package.json exist; the
# vendor-leak and hex gates are live from day one.
# ------------------------------------------------------------------------------
check: vendor-gate hex-gate
	@if [ -f go.mod ]; then \
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
	@if [ -f web/package.json ]; then \
		echo "--> svelte-check"; cd web && npx svelte-check --fail-on-warnings && \
		echo "--> eslint" && npx eslint . && \
		echo "--> vitest" && npx vitest run; \
	else \
		echo "skip: web checks (no web/package.json yet)"; \
	fi
	@$(MAKE) build
	@echo "check: GREEN"

build:
	@if [ -f web/package.json ]; then echo "--> web build"; cd web && npm run build; else echo "skip: web build"; fi
	@if [ -f go.mod ]; then echo "--> go build"; go build ./...; else echo "skip: go build"; fi

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
		--exclude-dir=__screenshots__ --exclude=package-lock.json --exclude=.gitkeep \
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
	@command -v ffmpeg >/dev/null || echo "TODO: install ffmpeg (brew install ffmpeg)"
	@if [ -f web/package.json ]; then cd web && npm install; fi
	@echo "setup: done (git hooks -> .githooks)"

# Playwright E2E against the demo stack. Real target lands with M0 web scaffold.
e2e:
	@if [ -f web/package.json ] && [ -d web/tests ]; then \
		cd web && npx playwright test; \
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

# Boot the full stack locally with deterministic seeded data and mocked AI
# (recorded fixtures) so agents can drive every flow offline. Lands in M0.
demo:
	@echo "TODO(M0): make demo — seeded local stack (app + worker + postgres + fixture episode, AI mocked)"
	@exit 1

# Local dev loop: Go API with live reload + Vite dev server. Lands in M0.
dev:
	@echo "TODO(M0): make dev — go run ./cmd/app + cd web && npm run dev"
	@exit 1
