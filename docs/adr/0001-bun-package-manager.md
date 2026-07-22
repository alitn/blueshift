# ADR 0001 — bun as the web package manager

**Status:** Accepted (human-approved 2026-07-22) · **Scope:** `web/` toolchain only

## Decision

Replace npm with **bun** for dependency installation and script running in `web/`
(`bun install`, `bun.lock`, `bun run <script>`). Node remains the runtime that test
tooling shells out to where it requires it (Playwright launches its own Node runners;
vitest binaries execute under their shebang runtime) — this ADR swaps the *package
manager*, not the JS runtime contract.

## Why

1. **Security:** bun does not execute lifecycle (postinstall) scripts by default — only
   packages allowlisted in `trustedDependencies`. Lifecycle scripts are the dominant npm
   supply-chain attack vector; npm needs `--ignore-scripts` discipline to match, and one
   forgotten flag reopens the hole.
2. **Speed:** installs are roughly an order of magnitude faster, which compounds across
   CI runs and agent loops.

## Consequences

- `web/package-lock.json` → `bun.lock` (committed); `trustedDependencies` explicitly
  lists any package that genuinely needs a postinstall (expected: none or esbuild-class).
- Makefile / CI swap `npm ci|run` → `bun install --frozen-lockfile` / `bun run`; CI adds
  `oven-sh/setup-bun` (pinned version) and drops the npm cache config.
- `make setup` documents bun as a prerequisite (`brew install oven-sh/bun/bun`).
- Rollback path: `package.json` stays npm-compatible; regenerating a package-lock and
  reverting the Makefile/CI lines restores npm in one commit.
