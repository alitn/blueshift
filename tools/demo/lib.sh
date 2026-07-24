#!/usr/bin/env bash
# tools/demo/lib.sh — shared helpers for make demo/dev/demo-down.
# Sourced by up.sh, dev.sh and down.sh. Never run directly.
#
# Standing rule (CLAUDE.md): the demo only ever touches resources it created.
# The one Postgres container it may start is named blueshift-demo-pg; teardown
# removes it only when a state marker proves this run created/used it. A
# DEMO_DATABASE_URL-backed run writes no marker, so it is never removed.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE_DIR="${BS_DEMO_STATE_DIR:-$REPO_ROOT/.demo}"
BIN_DIR="$STATE_DIR/bin"

# Fixed, offline-safe demo configuration. None of these are secrets: the demo is
# a local, seeded, throwaway stack. Real deployments set real values.
DEMO_HOST="127.0.0.1"
DEMO_PORT="${PORT:-8080}"
DEMO_WEB_PORT="${WEB_PORT:-5173}"
DEMO_BLOB_DIR="${BLOB_DIR:-$STATE_DIR/blob}"
DEMO_SESSION_SECRET="blueshift-demo-insecure-session-secret"
DEMO_DEV_PASSWORD="${DEV_PASSWORD:-blueshift-dev}"

# Postgres container coordinates (docker fallback). Port 5433 avoids clashing
# with a host Postgres on the default 5432.
PG_CONTAINER="blueshift-demo-pg"
PG_IMAGE="pgvector/pgvector:pg18"
PG_HOST_PORT="5433"
PG_PASSWORD="postgres"
DEMO_CONTAINER_DB_URL="postgres://postgres:${PG_PASSWORD}@${DEMO_HOST}:${PG_HOST_PORT}/postgres?sslmode=disable"

DB_URL=""       # set by demo_resolve_db
EP_ID=""        # set by demo_migrate_seed

log()  { printf '\033[36mdemo:\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[33mdemo:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31mdemo: error:\033[0m %s\n' "$*" >&2; exit 1; }

# tcp_reachable HOST PORT — true if a TCP connection opens.
tcp_reachable() {
  local host="$1" port="$2"
  (exec 3<>"/dev/tcp/${host}/${port}") >/dev/null 2>&1 && { exec 3>&- 3<&-; return 0; } || return 1
}

url_host() { sed -E 's#^[a-z]+://([^/@]*@)?([^:/?]+).*#\2#' <<<"$1"; }
url_port() {
  local p
  p="$(sed -nE 's#^[a-z]+://([^/@]*@)?[^:/?]+:([0-9]+).*#\2#p' <<<"$1")"
  printf '%s' "${p:-5432}"
}

have_docker() { command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; }

# demo_teardown — stop the app (and web dev server) this run started and remove
# the demo Postgres container iff a marker proves it is ours. Idempotent.
demo_teardown() {
  trap - EXIT INT TERM
  local f pid c
  for f in web app; do
    if [ -f "$STATE_DIR/$f.pid" ]; then
      pid="$(cat "$STATE_DIR/$f.pid" 2>/dev/null || true)"
      if [ -n "${pid:-}" ] && kill -0 "$pid" 2>/dev/null; then
        log "stopping $f (pid $pid)"
        kill "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
      fi
      rm -f "$STATE_DIR/$f.pid"
    fi
  done
  if [ -f "$STATE_DIR/pg_container" ] && have_docker; then
    c="$(cat "$STATE_DIR/pg_container")"
    log "removing demo Postgres container ($c)"
    docker rm -f "$c" >/dev/null 2>&1 || true
    rm -f "$STATE_DIR/pg_container"
  fi
}

# demo_resolve_db — set DB_URL from DEMO_DATABASE_URL (if reachable) -> docker
# container -> fail with instructions.
demo_resolve_db() {
  if [ -n "${DEMO_DATABASE_URL:-}" ]; then
    local h p; h="$(url_host "$DEMO_DATABASE_URL")"; p="$(url_port "$DEMO_DATABASE_URL")"
    if tcp_reachable "$h" "$p"; then
      log "using DEMO_DATABASE_URL ($h:$p)"
      DB_URL="$DEMO_DATABASE_URL"
      return
    fi
    warn "DEMO_DATABASE_URL is set but $h:$p is unreachable; falling back to a container"
  fi

  if have_docker; then
    if docker ps -a --format '{{.Names}}' | grep -qx "$PG_CONTAINER"; then
      log "reusing demo Postgres container ($PG_CONTAINER)"
      docker start "$PG_CONTAINER" >/dev/null 2>&1 || true
    else
      log "starting demo Postgres container ($PG_CONTAINER, $PG_IMAGE)"
      docker run -d --name "$PG_CONTAINER" \
        -e POSTGRES_PASSWORD="$PG_PASSWORD" \
        -p "${PG_HOST_PORT}:5432" \
        "$PG_IMAGE" >/dev/null \
        || die "could not start $PG_IMAGE (pull it first: docker pull $PG_IMAGE)"
    fi
    echo "$PG_CONTAINER" > "$STATE_DIR/pg_container"
    DB_URL="$DEMO_CONTAINER_DB_URL"

    log "waiting for Postgres to accept connections"
    local i
    for i in $(seq 1 60); do
      docker exec "$PG_CONTAINER" pg_isready -U postgres >/dev/null 2>&1 && return
      sleep 1
    done
    die "Postgres did not become ready within 60s"
  fi

  die "no Postgres available.
  Choose one:
    - export DEMO_DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=disable
    - or install Docker so the demo can run $PG_IMAGE as '$PG_CONTAINER'
      (macOS: brew install --cask docker; then open Docker and retry)."
}

# demo_build_binaries — build the Go binaries the demo runs. web_embed=1 also
# refreshes the embedded SPA via `make build` (needed by `make demo`; `make dev`
# serves the SPA via Vite, so it passes 0).
#
# BS_SKIP_BUILD=1 skips that `make build` and reuses a web build already produced
# this session — e.g. `BS_SKIP_BUILD=1 make e2e` right after a fresh `make build`
# (or `make check`, which ends in `make build`). The Go binaries below are always
# (re)built and embed the existing internal/webembed/dist. Default (flag unset)
# is unchanged: `make demo`/`make dev` always run `make build`, so local
# behaviour is byte-identical unless a caller opts in.
demo_build_binaries() {
  local web_embed="${1:-0}"
  if [ "$web_embed" = "1" ]; then
    if [ "${BS_SKIP_BUILD:-0}" = "1" ]; then
      log "BS_SKIP_BUILD=1 — reusing the existing web build (skipping make build)"
    else
      log "building web + go (make build)"
      make build >&2
    fi
  fi
  log "building demo binaries"
  go build -o "$BIN_DIR/app" ./cmd/app
  go build -o "$BIN_DIR/worker" ./cmd/worker
  go build -o "$BIN_DIR/demoseed" ./cmd/demoseed
}

# demo_migrate_seed — apply migrations, seed identities + the deterministic
# sample episode, then drive it through the REAL worker two-stage chain
# (ingest -> transcribe) so the sample boots 'ready' at current_stage=transcribe
# WITH a fake-engine transcript. The two stages run as separate blocking worker
# invocations with auto-advance OFF, so the seed is deterministic (no detached
# child races it) and fully transcribed before the demo banner prints. ASR runs
# in fake mode (ASR_ENGINE_MODE=fake) — deterministic, offline, no cost. Sets EP_ID.
demo_migrate_seed() {
  # Migrate CLI is version-locked to the go.mod require (v4.19.1) via `go run`, so
  # it can never drift from the library and needs no PATH binary. The `postgres`
  # build tag registers the pq driver (golang-migrate gates every driver behind a
  # build tag, and `go tool` cannot pass tags — hence `go run`). The cd makes
  # `go run` resolve the module and the relative `migrations` path.
  log "applying migrations"
  ( cd "$REPO_ROOT" && go run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate \
      -path migrations -database "$DB_URL" up )

  export DATABASE_URL="$DB_URL" BLOB_MODE="local" BLOB_DIR="$DEMO_BLOB_DIR"
  log "seeding dev identities + sample episode"
  EP_ID="$("$BIN_DIR/demoseed" -devseed "$REPO_ROOT/fixtures/dev-seed.sql" | tail -n 1)"
  [ -n "$EP_ID" ] || die "demoseed did not return a sample episode id"

  # Two-stage chain, driven explicitly one blocking step at a time. ingest runs
  # with auto-advance OFF (PIPELINE_STAGES makes it non-terminal, so it hands off
  # and stays 'processing' at ingest without spawning a detached transcribe), then
  # the fake transcribe stage finalizes the sample 'ready' with segments. The
  # uploaded-episode path (via the app) keeps auto-advance ON; this deterministic
  # drive is only so the seeded sample is READY-with-transcript at boot.
  log "running worker ingest -> transcribe (fake) for sample episode ($EP_ID)"
  WORKER_TRIGGER="exec" PIPELINE_STAGES="ingest,transcribe" ASR_ENGINE_MODE="fake" \
    PIPELINE_AUTO_ADVANCE="false" "$BIN_DIR/worker" "$EP_ID" ingest
  WORKER_TRIGGER="exec" PIPELINE_STAGES="ingest,transcribe" ASR_ENGINE_MODE="fake" \
    "$BIN_DIR/worker" "$EP_ID" transcribe
}

# demo_start_app — start the API server in the background, record its pid, and
# wait for it to accept connections. Env mirrors a dev deployment.
#
# PIPELINE_STAGES + ASR_ENGINE_MODE are worker-stage settings the app itself never
# reads (it only ever triggers the ingest entry stage), but they MUST be on the
# app's environment: the exec trigger spawns the ingest worker with the app's env
# inherited (cmd.Env=nil), and that worker's own exec trigger likewise hands its
# env to the transcribe worker it auto-advances into. So an UPLOADED episode's
# whole ingest->transcribe chain inherits the two-stage active chain and fake ASR
# from here. Omit PIPELINE_STAGES and the spawned ingest worker falls back to the
# default ingest-only chain — ingest stays terminal and nothing ever transcribes.
demo_start_app() {
  log "starting API server on http://$DEMO_HOST:$DEMO_PORT"
  ENV="dev" \
  AUTH_MODE="dev" \
  SESSION_SECRET="$DEMO_SESSION_SECRET" \
  DEV_PASSWORD="$DEMO_DEV_PASSWORD" \
  BLOB_MODE="local" \
  BLOB_DIR="$DEMO_BLOB_DIR" \
  DATABASE_URL="$DB_URL" \
  WORKER_TRIGGER="exec" \
  WORKER_BIN="$BIN_DIR/worker" \
  ASR_ENGINE_MODE="fake" \
  PIPELINE_STAGES="ingest,transcribe" \
  PORT="$DEMO_PORT" \
    "$BIN_DIR/app" &
  local pid=$!
  echo "$pid" > "$STATE_DIR/app.pid"

  local i
  for i in $(seq 1 60); do
    tcp_reachable "$DEMO_HOST" "$DEMO_PORT" && return
    kill -0 "$pid" 2>/dev/null || die "app exited before it became reachable"
    sleep 0.5
  done
  die "app did not become reachable within 30s"
}

demo_prepare_dirs() { mkdir -p "$STATE_DIR" "$BIN_DIR" "$DEMO_BLOB_DIR"; }
