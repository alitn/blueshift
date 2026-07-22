# Task: m0-design-refresh — apply the 2026-07-22 prototype readability refresh

**Milestone:** M0 (human updated design/) · **Type:** UI sweep · **Slug:** `m0-design-refresh`

## What changed (Architect diff of the prototype; DESIGN.md already updated)

1. **`text-faint`: `#6E6A63` → `#8C8880`** (1:1 global swap; `text-faintest #57534C` unchanged).
2. **Micro-type floor raised**: 8/8.5/9/9.5px sizes are gone; micro-labels now 10.5–11px,
   mono data 10.5–12px, breadcrumb 12px, wordmark suffix 10.5px. Mid sizes (13px+) unchanged.
3. **Font-role rule**: mono is reserved for data (timecodes, ids, filenames, counts,
   key-value); descriptive micro-labels/badges/hints switch to Archivo (font-ui) weight 600.
4. **Video-well hint alphas** raised: 0.30→0.42 primary, 0.18→0.28 secondary.

Ground truth: updated `design/project/Blue Shift Studio.dc.html` + revised DESIGN.md
typography section. `git diff HEAD -- "design/project/Blue Shift Studio.dc.html"` shows every
per-element value.

## Scope

1. **`web/src/lib/tokens.css`:** `--text-faint: #8C8880` (only hex home). Add/adjust any
   size tokens the theme carries.
2. **Sweep the built screens** (shell TopBar/StatusBar, login, Library table + dialogs,
   EmptyState, keyboard hint chips): apply the new sizes and the mono→Archivo-600 swaps per
   the prototype diff for the elements we have built. Data stays mono.
3. Update component tests asserting sizes/fonts/colors if any do.
4. Screenshot evidence to `.artifacts/screens/m0-design-refresh/` (1440×900: library seeded
   or mocked, login) — use the Playwright MCP browser against the Architect-managed dev
   server (HMR) per the new CLAUDE.md fast-UI-loop policy.

## Out of scope

Screens not yet built (episode/editor/render drawer/settings), prototype file itself,
DESIGN.md (done).

## Acceptance

- `make check` fully green; hex gate green (`#8C8880` only in tokens.css; zero `#6E6A63`
  anywhere in web/src).
- Grep proof: no `text-[8` / `text-[9` -px utilities remain in built components (or their
  equivalents), no mono-styled descriptive labels on the swept screens.
- Screenshots visibly match the refreshed prototype sections for the built screens.

## Evidence

Summary; diffstat; make check tail; grep proofs; screenshot paths.
