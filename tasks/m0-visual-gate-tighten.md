# Task: m0-visual-gate-tighten — screenshot tolerance must catch real drift

**Milestone:** M0 (AC3) · **Type:** test config fix · **Slug:** `m0-visual-gate-tighten`

## Finding (PR #1 run 29974819111 — the gate-proof caught this)

A deliberate visible drift (Library header `text-text-faint` → `text-accent`, ~3k changed
pixels) PASSED the visual gate: `maxDiffPixelRatio: 0.01` on a 1440×900 fullPage shot
permits ~13k pixels. The gate cannot catch the exact class of subtle token-misuse drift it
exists for.

## Scope

1. `web/playwright.config.ts`: replace the ratio with a strict absolute budget —
   `maxDiffPixels` in the low hundreds (start at 150; justify your final number). Keep
   `animations: 'disabled'`, `caret: 'hide'`; keep the per-pixel `threshold` default (0.2)
   which already absorbs anti-aliasing noise. Comment: budget sized to reject any visible
   drift while tolerating AA jitter; same-platform (linux CI) comparisons only.
2. Sanity-run the two visual specs against the local dev stack if feasible (make demo is
   bootable — coordinate: the Architect-managed dev server holds :5173/:8090; use your OWN
   transient demo on alternate ports via PORT/BS_* env if the harness allows, else state
   honestly that same-platform verification lands on the CI rerun).

## Acceptance

- make check green.
- The definitive proof is external: PR #1's next CI run must FAIL at the visual comparison
  (Architect will drive that after commit).

## Evidence

Summary; diff; chosen budget justification; any local run results.
