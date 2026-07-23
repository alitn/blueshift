# Task: m0-sveltekit-sync — explicit svelte-kit sync before svelte-check

**Milestone:** M0 · **Type:** build fix · **Slug:** `m0-sveltekit-sync`

## Finding (PR #1 run 29974264570)

On a fresh CI checkout, `svelte-check` fails: `Cannot read file 'web/.svelte-kit/tsconfig.json'`.
Cause: bun (ADR 0001) blocks lifecycle scripts, so SvelteKit's postinstall `svelte-kit sync`
never runs; nothing generates `.svelte-kit/` before svelte-check. Vite dev/build sync
implicitly, which is why local and the baselines workflow never hit it.

## Scope

In the Makefile's web check block, run `bunx --bun svelte-kit sync` immediately before
svelte-check (idempotent, fast, keeps the no-lifecycle-scripts security posture — explicit
beats postinstall). Comment the why (bun blocks postinstall by design; sync is the SvelteKit
step that generates .svelte-kit/tsconfig.json).

## Acceptance

- From a simulated fresh state (`rm -rf web/.svelte-kit`), `make check` green end to end.
- No trustedDependencies added; YAML/workflows untouched.

## Evidence

Summary; diff; fresh-state make check tail.
