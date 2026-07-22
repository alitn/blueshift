# Task: m0-dev-watch — auto-restart the Go app in the dev loop

**Milestone:** M0 tooling (human-approved) · **Type:** dev tooling · **Slug:** `m0-dev-watch`

## Scope

1. **`tools/demo/dev.sh`:** when a file watcher is available, watch `cmd/ internal/ go.mod`
   for `*.go` changes and rebuild+restart ONLY the Go app process the script itself started
   (same PID-marker discipline as demo teardown; never any other process). Watcher priority:
   `watchexec` if present, else `fswatch` (present on this machine), else no watching —
   print a one-line hint and behave exactly as today. Debounce rapid saves (~300ms);
   surface build errors in the dev log without killing the loop; Vite/HMR side untouched.
2. **Makefile `dev` target help text** updated (one line: Go hot-restart active when
   watcher present).
3. No new dependencies (uses whatever watcher exists on PATH).

## Acceptance

- `make check` green (script change only; `bash -n` clean).
- With fswatch present: editing a .go file under the running `make dev` rebuilds and
  restarts the app within ~2s; a compile error keeps the old process running and prints the
  error; `Ctrl-C`/teardown leaves no strays (only script-created PIDs killed).
- Without any watcher: behavior identical to today plus the hint line.

## Evidence

Summary; diffstat; make check tail; a short transcript of the restart-on-edit and
compile-error behaviors; open questions.
