# Task: m0-web-skeleton — SvelteKit scaffold, tokens, ui primitives, app shell

**Milestone:** M0 (docs/SPEC-M0.md §3) · **Type:** UI (partial DoD — see below) · **Slug:** `m0-web-skeleton`

## Goal

`web/` becomes a real SvelteKit app: strict TypeScript, `tokens.css` derived from
`design/DESIGN.md`, Tailwind themed exclusively from tokens, vendored shadcn-svelte
primitives on Bits UI in `components/ui/`, the app shell (top bar + status bar), and a
placeholder Library route. The web arm of `make check` activates and the build feeds the Go
embed.

## Design references (canonical)

`design/DESIGN.md` (the contract — token names/values are copied from it verbatim) and the
prototype `design/project/Blue Shift Studio.dc.html` (screens 01/03/05 for shell chrome).
PNG exports are not yet available; the prototype HTML is ground truth for the Reviewer.

## Allowed dependencies (declared stack, no ADR needed)

SvelteKit + `@sveltejs/adapter-static` + Vite + Svelte 5; TypeScript (strict); Tailwind CSS;
`bits-ui`; vendored shadcn-svelte component sources; `@fontsource*` packages for **Archivo,
IBM Plex Mono, Vazirmatn** (self-hosted — the app must work fully offline; **never** load
fonts from any CDN: remote font URLs would both break `make demo` and trip the vendor gate);
dev/test: `svelte-check`, `eslint` (+ svelte/ts plugins, flat config), `prettier`
(+ svelte/tailwind plugins), `vitest`, `@testing-library/svelte`, `jsdom`. Playwright/axe
land in m0-demo-seed, not here. Nothing else.

## Scope

1. **Scaffold.** SvelteKit in `web/`, TS `strict: true`, adapter-static with
   `fallback: 'index.html'`, `ssr = false` (SPA served by the Go embed). `npm run build`,
   `dev`, `preview` scripts; `svelte-check`, `eslint .`, `vitest run` all wired so the web
   arm of `make check` passes.
2. **`web/src/lib/tokens.css`.** Every token from DESIGN.md's tables as CSS custom properties
   under `:root` (`--bg-0…`, `--text-*`, `--accent*`, `--ok/--warn/--danger`, `--border-*`,
   `--overlay-*`, fonts, radii `--radius-1..4`, spacing scale, chrome heights). This is the
   **only** file with raw hex (hex gate). Include the font-face imports (fontsource) in the
   app css, not tokens.css.
3. **Tailwind theme** generated from tokens: theme colors reference the CSS variables
   (`var(--…)`) — no hex in the Tailwind config, no arbitrary-value color utilities anywhere.
4. **Vendored primitives** in `web/src/lib/components/ui/`: dialog, drawer (right slide-over
   per DESIGN.md), toast, tabs, select, tooltip, dropdown-menu — shadcn-svelte sources on
   Bits UI, restyled exclusively from tokens (bg-4 surfaces, border-control, radius-4/5,
   scrim overlay, 200ms motion + reduced-motion per DESIGN.md). Exported only via local
   wrappers (`index.ts` per component). No other UI kits.
5. **App shell** (`+layout.svelte` + `components/studio/`): top bar 52px (accent tick
   wordmark "BLUE SHIFT / STUDIO", breadcrumb slot, RENDER indicator placeholder chip (IDLE
   state, per screen 05), settings gear, avatar circle) and status bar 28px
   (`QUEUE 0 · STORAGE — · ENGINE — MS`, version right) — exact per DESIGN.md Component
   rules. Fonts: Archivo UI, IBM Plex Mono data.
6. **Library placeholder route** (`/`): shell + the first-run empty-state block from screen 05
   ("Upload your first master", dashed border card, disabled UPLOAD MASTER primary button) —
   static only; real Library with data lands in m0-library.
7. **Go embed wiring.** Refactor `internal/webembed` so the HTTP handler takes an `fs.FS`
   (embedded `all:dist` in prod; `fstest.MapFS` in tests). Gitignore
   `internal/webembed/dist/` contents (keep `.gitkeep`), remove the committed placeholder
   `index.html`, and make the `build` target: web build → rsync/cp `web/build/` →
   `internal/webembed/dist/` → `go build`. `make check` still green from a clean tree.
8. **Tests (same change):** vitest + testing-library for logic-bearing pieces: toast
   store/queue behavior, tabs keyboard switching, drawer open/close + focus trap smoke, shell
   renders wordmark/status bar, breadcrumb slot. Update Go webembed tests for the fs.FS
   injection. Everything race-/lint-/type-clean.

## Definition of Done — deferred items (Architect-recorded)

Playwright E2E, visual baselines, token-conformance computed-style assertions, axe smoke,
and RTL DOM assertions land in **m0-demo-seed** (the harness task). This task's UI DoD =
component tests + hex/vendor gates + screenshot evidence:

9. **Screenshot evidence.** Run `npm run preview` (or dev), capture the Library placeholder
   at 1440×900 and 1280×800 to `.artifacts/screens/m0-web-skeleton/` (any headless tool you
   have; a one-off node script using vite preview + playwright is NOT available yet — if you
   have no headless browser, capture via `npx playwright` ONLY if already installed, else
   report the gap honestly and the Reviewer will judge chrome from code + tokens).

## Out of scope

Real Library data/status (m0-library), upload flow (m0-upload), auth UI (m0-auth), Playwright
config (m0-demo-seed), any additional pages.

## Acceptance

- `make check` fully green with the web arm active (svelte-check --fail-on-warnings, eslint,
  vitest, web build, hex gate, vendor gate).
- `make build` produces a Go binary that serves the built SPA shell at `/` (verify with
  `go run ./cmd/app` + curl: `/` returns the built index, unknown path falls back).
- No raw hex outside `tokens.css`; every color/font/radius/spacing in components references
  tokens; no CDN/network fetches at runtime (fully offline).
- Shell chrome matches DESIGN.md values (heights 52/28, wordmark spec, borders, fonts).

## Evidence to return

Summary + deviations; `git diff --stat` + `git status --short`; tail of `make check`;
screenshot paths (or the honest gap); open questions.
