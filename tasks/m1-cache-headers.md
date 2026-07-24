# Task: m1-cache-headers — correct HTTP caching for the SPA shell and assets

**Milestone:** M1 (human-hit bug 2026-07-24: stale shell hid a deployed feature) · **Type:** backend small · **Slug:** `m1-cache-headers`

## Problem

The embedded SPA is served with NO cache headers. Browsers heuristically cache the HTML
shell, so after a deploy users keep running the OLD app indefinitely (the human saw no
moments rail on either episode until a hard refresh). Hashed immutable assets also lack
headers (harmless for staleness, wasteful for performance).

## Scope (Go static serving — internal/webembed / the app's static handler)

1. **HTML shell (`/`, `/index.html`, and every SPA-fallback response):**
   `Cache-Control: no-cache` (forced revalidation) + strong `ETag` so unchanged shells
   still 304. (no-cache-with-ETag over no-store: revalidation is cheap and keeps
   back/forward snappy. Document the choice.)
2. **`/_app/immutable/**` (content-hashed filenames):**
   `Cache-Control: public, max-age=31536000, immutable`.
3. **Other static files (favicons etc.):** modest `max-age` (e.g. 3600) — judgment,
   document.
4. **Tests:** handler tests asserting the exact header per path class (shell, immutable
   asset, fallback route like /episode/xyz); e2e smoke unaffected. Verify /healthz,
   /readyz and /api/** are untouched (no caching added to APIs — confirm none present
   today or that they already send appropriate no-store; add `Cache-Control: no-store`
   to /api/** responses if absent, as a defensive default — flag if that requires
   touching many handlers vs one middleware).

## Acceptance

- make check green; header tests prove all three classes.
- Reviewer verifies: fallback (deep-link) responses carry the shell policy; immutable
  only on hashed paths; APIs no-store; no behavior change otherwise.
- Architect post-deploy: curl -I the shell (sees no-cache+ETag, 304 on revalidate) and an
  immutable asset (sees immutable). Human's stale-shell class of bug is dead.

## Evidence

Summary; diffs; header transcript; open questions.
