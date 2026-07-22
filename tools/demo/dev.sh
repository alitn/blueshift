#!/usr/bin/env bash
# tools/demo/dev.sh — local dev loop. Invoked by `make dev`. Boots the same
# seeded backend as `make demo`, then runs the Vite dev server (hot reload) in
# the foreground. Vite proxies /api -> the Go app (see web/vite.config.ts), so
# the SPA is edited live while every API/upload/proxy call hits the real server.

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
cd "$REPO_ROOT"

demo_prepare_dirs
trap demo_teardown EXIT INT TERM

demo_resolve_db
echo "$DB_URL" > "$STATE_DIR/db_url"
echo "$DEMO_BLOB_DIR" > "$STATE_DIR/blob_dir"

demo_build_binaries 0   # 0 = no embed; the SPA is served by Vite in dev
demo_migrate_seed
demo_start_app

cat >&2 <<BANNER

  Blueshift Studio dev loop is up.
    API:      http://$DEMO_HOST:$DEMO_PORT   (Go server)
    Web:      http://$DEMO_HOST:$DEMO_WEB_PORT   (Vite dev, proxies /api -> API)
    Sign in:  dev-approver@blueshift.local  /  $DEMO_DEV_PASSWORD
    Sample:   $EP_ID  (READY)
  Edit web/src for hot reload. Stop with Ctrl-C.

BANNER

# Vite dev server in the foreground. Its /api proxy target follows the app port.
( cd web && BS_API_PORT="$DEMO_PORT" npm run dev -- --port "$DEMO_WEB_PORT" --strictPort ) &
WEB_PID=$!
echo "$WEB_PID" > "$STATE_DIR/web.pid"
wait "$WEB_PID"
