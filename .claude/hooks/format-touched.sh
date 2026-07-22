#!/bin/sh
# PostToolUse hook (Write|Edit): run the matching formatter on the touched file.
# Receives the tool payload as JSON on stdin. Always exits 0 — formatting is
# best-effort here; make check is the enforcing gate.

file=$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("tool_input",{}).get("file_path",""))' 2>/dev/null)
[ -n "$file" ] && [ -f "$file" ] || exit 0

case "$file" in
  *.go)
    command -v gofmt >/dev/null 2>&1 && gofmt -w "$file"
    ;;
  *.ts|*.js|*.svelte|*.css|*.json|*.html)
    root="${CLAUDE_PROJECT_DIR:-.}"
    if [ -x "$root/web/node_modules/.bin/prettier" ]; then
      "$root/web/node_modules/.bin/prettier" --write "$file" >/dev/null 2>&1
    fi
    ;;
esac
exit 0
