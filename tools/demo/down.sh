#!/usr/bin/env bash
# tools/demo/down.sh — tear down whatever `make demo`/`make dev` started on this
# machine, and nothing else. Invoked by `make demo-down`. Safe when nothing is
# up. Removes the demo Postgres container only if a state marker proves this
# demo created it (a DEMO_DATABASE_URL-backed run leaves no marker).

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

demo_teardown
rm -f "$STATE_DIR/db_url" "$STATE_DIR/blob_dir"
log "demo torn down"
