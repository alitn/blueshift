#!/usr/bin/env bash
# tools/demo/dev.sh — local dev loop. Invoked by `make dev`. Boots the same
# seeded backend as `make demo`, then runs the Vite dev server (hot reload) in
# the foreground. Vite proxies /api -> the Go app (see web/vite.config.ts), so
# the SPA is edited live while every API/upload/proxy call hits the real server.
#
# Go hot-restart: when a file watcher is on PATH, edits to the Go sources this
# script builds from (cmd/ internal/ go.mod) rebuild BOTH the app and worker
# binaries and restart ONLY the app process this script started (the worker is
# spawned per job, so a fresh binary is enough — no process to restart). Same
# PID-marker discipline as teardown, never any other process. A build failure in
# either binary is printed and leaves the old app running and both old binaries
# in place (they stay a coherent pair — the app is restarted only when BOTH
# rebuild). The Vite/HMR side is untouched. With no watcher on PATH the loop
# behaves exactly as before, plus a one-line hint. Priority: watchexec, fswatch.

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
cd "$REPO_ROOT"

# --- Go hot-restart -----------------------------------------------------------
DEV_WATCH_FIFO="$STATE_DIR/gowatch.fifo"
DEV_WATCHER=""                                   # watcher kind in use ("" = none)
DEV_WATCH_STATUS="manual restart (no watcher on PATH)"

# dev_pick_watcher — echo the watcher we'll drive, honouring the required
# priority (watchexec first, then fswatch), or nothing when neither is on PATH.
dev_pick_watcher() {
  if command -v watchexec >/dev/null 2>&1; then echo watchexec
  elif command -v fswatch >/dev/null 2>&1; then echo fswatch
  fi
}

# dev_stop_app — stop the app THIS script currently owns (app.pid) and wait for
# it to release its port before we rebind, SIGKILL as a last resort. The app
# shuts down gracefully on SIGTERM and Go sets SO_REUSEADDR, so the fresh binary
# binds the same port cleanly once the old PID is gone. Never fatal.
dev_stop_app() {
  local pid i
  [ -f "$STATE_DIR/app.pid" ] || return 0
  pid="$(cat "$STATE_DIR/app.pid" 2>/dev/null || true)"
  [ -n "${pid:-}" ] || return 0
  kill -0 "$pid" 2>/dev/null || return 0
  kill "$pid" 2>/dev/null || true
  for i in $(seq 1 40); do
    kill -0 "$pid" 2>/dev/null || return 0
    sleep 0.05
  done
  kill -9 "$pid" 2>/dev/null || true
  sleep 0.1
}

# dev_rebuild_restart — rebuild BOTH the app and worker binaries, then, only if
# BOTH built, swap them into place and restart the app (the worker just needs
# fresh bytes for its next per-job spawn). A failure in either build prints which
# one broke and leaves the current app running and both current binaries intact,
# so app+worker never diverge. Runs once per debounced batch of source changes.
# Never fatal to the watch loop (demo_start_app runs in a subshell so its
# die/exit can't kill the loop). Builds go to *.new temp paths first so a
# half-failed rebuild never leaves a mismatched pair in $BIN_DIR.
dev_rebuild_restart() {
  local app_out worker_out app_ok=1 worker_ok=1
  log "go change detected — rebuilding app + worker"
  app_out="$(go build -o "$BIN_DIR/app.new" ./cmd/app 2>&1)" || app_ok=0
  worker_out="$(go build -o "$BIN_DIR/worker.new" ./cmd/worker 2>&1)" || worker_ok=0
  if [ "$app_ok" -eq 0 ] || [ "$worker_ok" -eq 0 ]; then
    if [ "$app_ok" -eq 0 ] && [ "$worker_ok" -eq 0 ]; then
      warn "build failed (app + worker) — not restarting; keeping the running app and both current binaries:"
    elif [ "$app_ok" -eq 0 ]; then
      warn "build failed (app) — not restarting; keeping the running app and both current binaries:"
    else
      warn "build failed (worker; app built OK) — not restarting the app; keeping the running app and both current binaries:"
    fi
    [ "$app_ok" -eq 0 ] && printf '%s\n' "$app_out" >&2
    [ "$worker_ok" -eq 0 ] && printf '%s\n' "$worker_out" >&2
    rm -f "$BIN_DIR/app.new" "$BIN_DIR/worker.new"
    return 0
  fi
  # Both built: activate the fresh pair, then restart the app. The worker's next
  # spawn (WORKER_BIN) now runs fresh code without any process restart.
  mv -f "$BIN_DIR/app.new" "$BIN_DIR/app"
  mv -f "$BIN_DIR/worker.new" "$BIN_DIR/worker"
  dev_stop_app
  if ! ( demo_start_app ); then
    warn "app did not come back up after rebuild — will retry on the next change"
    return 0
  fi
  log "app restarted (worker binary refreshed for its next spawn)"
}

# dev_start_watch — if a watcher is available, spawn it (producer) and a rebuild
# loop (consumer) joined by a FIFO. Both get PID markers so teardown reaps them
# and nothing else. Debounces ~300ms and matches only *.go and go.mod.
dev_start_watch() {
  DEV_WATCHER="$(dev_pick_watcher)"
  if [ -z "$DEV_WATCHER" ]; then
    warn "no file watcher on PATH (watchexec or fswatch) — Go hot-restart is OFF; restart with a fresh 'make dev' after Go edits"
    return 0
  fi
  rm -f "$DEV_WATCH_FIFO"
  mkfifo "$DEV_WATCH_FIFO"
  case "$DEV_WATCHER" in
    watchexec)
      # --exts go,mod matches *.go and go.mod; printf emits one line per batch.
      watchexec --debounce 300ms --quiet \
        --watch cmd --watch internal --watch go.mod --exts go,mod \
        -- printf 'go\n' > "$DEV_WATCH_FIFO" 2>/dev/null &
      ;;
    fswatch)
      # -o = one line per batch; --latency debounces; the include gate keeps
      # only *.go and go.mod, so unrelated files never trigger a rebuild.
      fswatch -o --latency 0.3 -e '.*' -i '\.go$' -i 'go\.mod$' \
        cmd internal go.mod > "$DEV_WATCH_FIFO" 2>/dev/null &
      ;;
  esac
  echo $! > "$STATE_DIR/gowatch_src.pid"
  ( while IFS= read -r _; do dev_rebuild_restart; done < "$DEV_WATCH_FIFO" ) &
  echo $! > "$STATE_DIR/gowatch.pid"
  DEV_WATCH_STATUS="hot-restart on save ($DEV_WATCHER)"
  log "Go $DEV_WATCH_STATUS — watching cmd/ internal/ go.mod"
}

# dev_teardown — reap the watcher (producer + consumer) this run started, then
# delegate to demo_teardown for the app, Vite dev server and demo Postgres. Same
# marker discipline: verify each PID is ours and still alive before killing it.
# The consumer can be blocked in an in-flight `go build` when teardown arrives
# via SIGTERM (make demo-down), which defers its SIGTERM; so escalate to SIGKILL
# after a short grace to guarantee it is gone (a Ctrl-C group-signal kills the
# build outright, so this only bites the non-terminal teardown path).
dev_teardown() {
  trap - EXIT INT TERM
  local f pid j
  for f in gowatch_src gowatch; do
    if [ -f "$STATE_DIR/$f.pid" ]; then
      pid="$(cat "$STATE_DIR/$f.pid" 2>/dev/null || true)"
      if [ -n "${pid:-}" ] && kill -0 "$pid" 2>/dev/null; then
        log "stopping $f (pid $pid)"
        kill "$pid" 2>/dev/null || true
        for j in $(seq 1 40); do kill -0 "$pid" 2>/dev/null || break; sleep 0.05; done
        kill -9 "$pid" 2>/dev/null || true
      fi
      rm -f "$STATE_DIR/$f.pid"
    fi
  done
  # A rebuild interrupted by teardown can leave *.new staging binaries; they are
  # ours and gitignored, but clean them so $BIN_DIR is tidy for the next run.
  rm -f "$DEV_WATCH_FIFO" "$BIN_DIR/app.new" "$BIN_DIR/worker.new"
  demo_teardown
}
# --- end Go hot-restart -------------------------------------------------------

demo_prepare_dirs
trap dev_teardown EXIT INT TERM

demo_resolve_db
echo "$DB_URL" > "$STATE_DIR/db_url"
echo "$DEMO_BLOB_DIR" > "$STATE_DIR/blob_dir"

demo_build_binaries 0   # 0 = no embed; the SPA is served by Vite in dev
demo_migrate_seed
demo_start_app
dev_start_watch

cat >&2 <<BANNER

  Blueshift Studio dev loop is up.
    API:      http://$DEMO_HOST:$DEMO_PORT   (Go server)
    Web:      http://$DEMO_HOST:$DEMO_WEB_PORT   (Vite dev, proxies /api -> API)
    Go:       $DEV_WATCH_STATUS
    Sign in:  dev-approver@blueshift.local  /  $DEMO_DEV_PASSWORD
    Sample:   $EP_ID  (READY)
  Edit web/src for hot reload. Stop with Ctrl-C.

BANNER

# Vite dev server in the foreground. Its /api proxy target follows the app port.
# `bun --bun run` forces the bun runtime so vite's native rollup/esbuild deps
# match bun's arch (ADR 0001).
( cd web && BS_API_PORT="$DEMO_PORT" bun --bun run dev -- --port "$DEMO_WEB_PORT" --strictPort ) &
WEB_PID=$!
echo "$WEB_PID" > "$STATE_DIR/web.pid"
wait "$WEB_PID"
