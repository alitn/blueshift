# Task: m0-bun-migration — bun replaces npm for web/ (ADR 0001)

**Milestone:** M0 tooling (ADR 0001, human-approved) · **Type:** build tooling · **Slug:** `m0-bun-migration`

## Scope (bun = package manager + script runner; not a runtime swap)

1. **`web/`:** generate `bun.lock` with bun 1.3.x (`bun install`); delete
   `package-lock.json`; add `"trustedDependencies"` to package.json ONLY for packages that
   genuinely need lifecycle scripts (verify what breaks without them — expected candidates:
   none, or esbuild/svelte-native binaries; document each one added and why). Confirm
   installs are script-free otherwise.
2. **Makefile:** every `npm ci|install|run|npx` under web → bun equivalents
   (`bun install --frozen-lockfile`, `bun run build`, `bunx --bun=false playwright …` or
   plain `bunx` where the tool runs fine — verify svelte-check, eslint, vitest, playwright
   each actually run and pass; if one needs node explicitly, invoke it the way that works
   and note it). `make setup` checks for bun (`brew install oven-sh/bun/bun` hint).
3. **CI (`pr.yml`, `deploy.yml`):** replace setup-node/npm-cache with `oven-sh/setup-bun@v2`
   (pin bun version to match local major); adjust install/run/playwright-install lines.
   Keep node present if any tool requires it (setup-node can stay alongside if needed —
   state which tools required it).
4. **Dockerfile stage 1:** `oven/bun` (pinned) or install bun into the node image —
   whichever keeps the web build reproducible; document the choice.
5. **tools/demo/*.sh:** any npm/npx references → bun equivalents.
6. **Vendor gate sanity:** `bun.lock` is inside web/ — confirm the gate's exclusions cover
   it or extend `--exclude=bun.lock` the same way package-lock.json was excluded (Makefile
   change allowed for that flag only).

## Acceptance

- Fresh `web/node_modules` wipe → `bun install --frozen-lockfile` → **full `make check`
  green end to end** (svelte-check, eslint, vitest, build all executed via the new paths).
- `npx playwright test --list` equivalent works under the new invocation.
- No package-lock.json anywhere; bun.lock committed; lifecycle-script posture documented in
  the report (which trustedDependencies exist and why).
- CI YAMLs parse; Dockerfile builds locally if Docker present (else reasoned, per precedent).

## Evidence

Summary + deviations (esp. anything that still needs node and why); diffstat; make check
tail from a clean install; trustedDependencies list + justification; open questions.
