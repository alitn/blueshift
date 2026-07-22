#!/usr/bin/env bash
# tools/demo/up.sh — boot the full stack locally, offline, with seeded data.
# Invoked by `make demo`. Serves the embedded SPA from the Go binary. Blocks in
# the foreground so Playwright's webServer (and a human) can drive it; Ctrl-C or
# SIGTERM tears down exactly what this run started.

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
cd "$REPO_ROOT"

demo_prepare_dirs
trap demo_teardown EXIT INT TERM

demo_resolve_db
echo "$DB_URL" > "$STATE_DIR/db_url"
echo "$DEMO_BLOB_DIR" > "$STATE_DIR/blob_dir"

demo_build_binaries 1   # 1 = refresh the embedded SPA
demo_migrate_seed
demo_start_app

cat >&2 <<BANNER

  Blueshift Studio demo is up.
    URL:      http://$DEMO_HOST:$DEMO_PORT
    Sign in:  dev-approver@blueshift.local  /  $DEMO_DEV_PASSWORD   (approver)
              dev-editor@blueshift.local    /  $DEMO_DEV_PASSWORD   (editor)
    Sample:   $EP_ID  (READY)
  Stop with Ctrl-C, or run: make demo-down

BANNER

# Block on the app so this stays a foreground service; the trap cleans up on
# Ctrl-C / SIGTERM (Playwright's webServer sends SIGTERM when the run ends).
wait "$(cat "$STATE_DIR/app.pid")"
