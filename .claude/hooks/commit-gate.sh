#!/bin/sh
# PreToolUse hook (Bash): if the command is a git commit, require `make check`
# to pass first. Exit 2 blocks the tool call and feeds stderr back to the agent.
# This is the deterministic gate: nothing red can be committed, regardless of
# what any agent believes.

cmd=$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("tool_input",{}).get("command",""))' 2>/dev/null)

case "$cmd" in
  *"git commit"*|*"git "*" commit"*)
    root="${CLAUDE_PROJECT_DIR:-.}"
    log=$(mktemp)
    if ! make -C "$root" check >"$log" 2>&1; then
      echo "BLOCKED: make check failed — commit refused. Output tail:" >&2
      tail -n 40 "$log" >&2
      rm -f "$log"
      exit 2
    fi
    rm -f "$log"
    ;;
esac
exit 0
