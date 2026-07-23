# Task: m0-ci-lint-pin — pin golangci-lint install in CI (checksum flake blocks all PRs)

**Milestone:** M0 · **Type:** CI fix · **Slug:** `m0-ci-lint-pin`

## Finding (live, PR #1 run 29971687672, 3/3 attempts)

`Install golangci-lint` in pr.yml uses the curl|sh installer fetching an unpinned latest;
it fails `hash_sha256_verify checksum ... did not verify` on every attempt, killing every PR
check before make check runs. CI is red for all PRs until fixed.

## Scope

Replace the installer step in `.github/workflows/pr.yml` (and `baselines.yml` if it has one —
check) with the official `golangci/golangci-lint-action` pinned to the version we run locally
(v2.12.x — check `golangci-lint version` locally and pin that), configured to install-only if
possible or run as its own step consistent with `make check`'s expectation that the binary is
on PATH (`install-mode: binary` + `skip-cache` decisions justified in a comment; ensure
`make check` still finds it). Alternative if the action can't do install-only cleanly: keep
curl installer but pin the exact version tag instead of latest. Pick the more reliable and
explain.

## Acceptance

- YAML parses; make check green locally.
- Reviewer confirms the pinned version matches local, and that make check's lint step will
  use the installed binary in CI.

## Evidence

Summary; diff; validation.
