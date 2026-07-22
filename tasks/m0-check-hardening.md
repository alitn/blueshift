# Task: m0-check-hardening — make check must fail on ANY red step

**Milestone:** M0 (supports AC4/AC6) · **Type:** build tooling · **Slug:** `m0-check-hardening`

## Problem

The Go block in the `check` recipe is one shell invocation whose exit status is that of the
last command; a nonzero `go vet` or `golangci-lint` does not fail the target. The commit gate
and CI therefore cannot be trusted to block on lint/vet failures. Found during m0-db-baseline.

## Scope

1. In `Makefile` `check` (and any other multi-command recipe blocks: `build`, future web block),
   make every step's failure fail the target: `set -e;` at the top of each shell block (plus
   `&&`-chaining where clearer). Keep output labels (`--> go vet` etc.) unchanged.
2. Also make `golangci-lint` **required** when `go.mod` exists in CI context but keep the local
   soft-warn only if the binary is genuinely absent (current behavior) — do not change that
   semantic, just make sure that when it runs and fails, `check` fails.
3. Prove it: temporarily introduce (a) a vet-failing construct, (b) a lint-failing construct —
   observe `make check` exit nonzero for each — then remove them. Include the command output
   evidence in your report. The tree you leave behind must be clean and green.

## Out of scope

Any change to what `make check` checks (no new steps, no removed steps), gates' grep lists,
hooks, CI workflow files.

## Acceptance

- `make check` exits nonzero if any of: gofmt non-empty, vet fails, lint fails (when installed),
  go tests fail, web steps fail (when present), build fails, either gate fails.
- Final tree: `make check` GREEN, `git diff` shows only the Makefile change.

## Evidence to return

Summary; the red-run proof output (vet + lint) with the seeded defect described; diffstat;
tail of final green `make check`.
