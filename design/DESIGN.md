# DESIGN.md — the design contract

**Status: ACTIVE.** Transcribed by the Architect from the Claude Design export in
`design/project/Blue Shift Studio.dc.html` (5 screens: 01 Episode default, 02 Moment expanded,
03 Library, 04A Render drawer, 04B Settings, 05 First-run + edge states). This file is **the
single source of visual truth**. `web/src/lib/tokens.css` is generated to match this file and is
the only place raw hex values may appear (enforced by the hex gate in `make check`).

Rules of the contract:

- Every UI task spec references the relevant screen in `design/screens/` (PNG exports pending —
  until they land, the prototype HTML sections named above are the canonical reference).
- If `design/` and a task spec conflict, the Architect resolves the conflict and updates
  this file **first**, then the spec.
- When the human drops updated exports, the Architect diffs them against current baselines and
  opens tasks for the deltas. Baseline updates in `web/tests/__screenshots__/` are authorized
  only by the Architect.

## Color tokens

Dark-only UI. Token names are canonical; Tailwind theme and `tokens.css` are generated from
this table. Alpha-composite colors are expressed as rgba over the surface they sit on.

### Background ramp

| Token | Value | Usage |
|-------|-------|-------|
| `bg-page` | `#0C0C0D` | page/body behind the app frame |
| `bg-0` | `#09090A` | 9:16 phone preview wells |
| `bg-1` | `#0A0A0B` | video/proxy playback wells; scrim base |
| `bg-2` | `#141414` | app canvas — main content background |
| `bg-3` | `#1A1A1B` | top bar, status bar, side panels (Moments rail), drawers |
| `bg-4` | `#212123` | cards, popovers, avatars, dropdown surfaces |
| `bg-5` | `#2A2A2C` | progress-bar tracks, deepest raised fills |
| `bg-film-1` | `#0B0B0C` | filmstrip frame fill (dark shot) |
| `bg-film-2` | `#111114` | filmstrip frame fill (next shot) |
| `bg-film-flash` | `#17171A` | filmstrip flash-frame fill |

### Text ramp

| Token | Value | Usage |
|-------|-------|-------|
| `text-primary` | `#EDEBE6` | headings, primary values, active nav |
| `text-body` | `#DDDAD3` | transcript body, long-form content |
| `text-muted` | `#9A968E` | secondary labels, section headers, inactive nav |
| `text-faint` | `#8C8880` | tertiary metadata, column headers, hints (2026-07-22 refresh: lightened from `#6E6A63` for readability) |
| `text-faintest` | `#57534C` | separators (·, ▸), lowest-emphasis glyphs |
| `text-on-accent` | `#F4F6FA` | text on accent-filled buttons |
| `text-caption` | `#FFFFFF` | burned-caption preview text on video |

### Accent

| Token | Value | Usage |
|-------|-------|-------|
| `accent` | `#4E7FC2` | primary buttons, progress fills, playhead, selection borders, active pipeline step, brand tick |
| `accent-bright` | `#6493D1` | button hover, links, active timecode/labels (IN/OUT, RENDER %) |
| `accent-soft` | `#8FB0DC` | link hover |
| `accent-selection` | `rgba(78,127,194,0.35)` | text ::selection |
| `accent-wash-08` | `rgba(78,127,194,0.08)` | active row background (Library render row) |
| `accent-wash-12` | `rgba(78,127,194,0.12)` | active top-bar indicator fill |
| `accent-wash-14` | `rgba(78,127,194,0.14)` | moment-span highlight in transcript |
| `accent-wash-16` | `rgba(78,127,194,0.16)` | selected waveform region fill |
| `accent-wash-18` | `rgba(78,127,194,0.18)` | IN CLIP chip fill, active filter chip, active moment tab |
| `accent-border` | `rgba(78,127,194,0.55)` | active/selected control borders (prototype uses 0.5–0.6; canonical 0.55) |

### Semantic

| Token | Value | Usage |
|-------|-------|-------|
| `ok` | `#5BA97B` | READY, PASS, confirmed dots, finished renders |
| `warn` | `#D0973B` | CHECKING / NEEDS CONFIRM dots, SAMPLE & SHOW badges, flash-frame markers; glossary dotted underline `rgba(208,151,59,0.55)`; border washes 0.4 / 0.45 / 0.65 |
| `danger` | `#C4554D` | FAILED, fidelity FAIL, RETRY; border wash 0.55 (hover 0.85); mismatch highlight `rgba(196,85,77,0.22)` |

### Borders & overlays (white-alpha system)

| Token | Value | Usage |
|-------|-------|-------|
| `border-hairline` | `rgba(255,255,255,0.06)` | row separators inside lists |
| `border-subtle` | `rgba(255,255,255,0.08)` | panel/section borders, bar borders |
| `border-default` | `rgba(255,255,255,0.10)` | card & frame borders, pipeline pending fill |
| `border-strong` | `rgba(255,255,255,0.12)` | interactive chip/control borders, avatar rings |
| `border-control` | `rgba(255,255,255,0.14)` | secondary-button borders, popover borders |
| `border-hover` | `rgba(255,255,255,0.28)` | hover for `border-strong` controls (0.30 for `border-control`) |
| `overlay-scrim` | `rgba(10,10,11,0.58)` | drawer/modal backdrop |
| `overlay-timecode` | `rgba(0,0,0,0.62)` | timecode chip background on video |
| `overlay-caption` | `rgba(0,0,0,0.55)` | caption box background on video (brand kit "BLACK 55%") |
| `wave-unplayed` | `rgba(237,235,230,0.20)` | waveform bars, unplayed |
| `step-done` | `rgba(237,235,230,0.45)` | pipeline step, completed |
| `hover-row` | `rgba(255,255,255,0.025)` | row hover wash |
| `guide-dashed` | `rgba(255,255,255,0.09)` | caption-safe-area dashed guides |

## Typography

| Token | Stack | Usage |
|-------|-------|-------|
| `font-ui` | `'Archivo', sans-serif` (400/500/600/700) | UI labels, buttons, English headings |
| `font-mono` | `'IBM Plex Mono', monospace` (400/500/600) | timecodes, IDs, metadata, section labels, status bar — numbers always `font-variant-numeric: tabular-nums` |
| `font-fa` | `'Vazirmatn', sans-serif` (400/500/600/700) | Persian content: transcript, titles, captions, names. Resolved per-language via `/internal/lang`; `font-fa` is the `fa` registry entry, not a global. |

Scale (px) — **2026-07-22 readability refresh**: the micro-type floor rose from 8–9.5px to
10–12px, and mono is now reserved for **data** (timecodes, IDs, filenames, counts,
key-value metadata); descriptive micro-labels (status words, hints, badges like
PROXY 720P / RENDER / PROXY PLAYBACK / "AUTO FA · CLICK WORD TO SEEK") use `font-ui`
(Archivo) **weight 600**. The updated prototype is ground truth for per-element values.

- **Micro-labels (Archivo 600):** 10.5–11px, uppercase, letter-spacing 0.06–0.2em; wordmark suffix 10.5px @ 0.26em.
- **Mono data:** 10.5–12px (IDs, timecodes, counts); player timecode 13.5–15px weight 500.
- **UI text:** 11–13.5px; buttons 10.5–11px weight 600 @ 0.1em tracking; breadcrumb 12px; wordmark 13.5px weight 700.
- **Persian content:** transcript body 14.5px / line-height 2.0 (condensed panels 12.5px / 1.95); moment quotes 12px / 1.9; names 12px; popover title 15px weight 600; caption preview 13.5px weight 600 / 1.8.
- Body base: 13px `text-primary` on `bg-2`.
- **Video-well hint text:** primary `rgba(237,235,230,0.42)`, secondary `rgba(237,235,230,0.28)` (raised from 0.30/0.18).

## Spacing, radii, elevation

- **Spacing scale:** 4 / 6 / 8 / 10 / 12 / 14 / 16 / 18 / 20 / 24 px. Card padding 12–14; panel padding 12–20; bar padding 0 16–24.
- **Fixed chrome:** top bar 52px; panel headers 40–44px; status bar 28px (34px on Library); Library rows 62px; moment nav rail 52px wide.
- **Radii:** 2px micro (progress tracks, mini chips); 3px badges/chips/timecode overlays; 4px buttons, inputs, controls; 5px cards, panels, popovers, app frame; 50% avatars/dots.
- **Elevation:** no shadows — depth via background ramp + borders + scrim only.

## Component rules

- **Buttons.** Primary: `accent` fill, `text-on-accent`, weight 600, 0.1em tracking, radius 4, hover `accent-bright`; disabled = opacity 0.35 + `cursor:not-allowed`. Secondary: transparent, `border-control`, `text-primary`, hover `border-hover`. Ghost: text-only `text-muted`, hover `text-primary`. Danger (RETRY): `danger` text + danger border wash.
- **Status dots:** 6–7px circles in `ok`/`warn`/`danger`; always paired with a mono micro-label in the same color.
- **Cards (moment cards, render items):** `bg-4`, `border-default`, radius 5, padding 12. Persian quote inside: 2px solid `border-strong` inline-start rule, 10px padding-inline-start.
- **Pipeline steps:** five 22×4px radius-2 bars: `step-done` done · `accent` active · `border-default` pending · `danger` failed. Mono 8.5px stage label below, colored to state.
- **Chips/badges:** mono 8–9px uppercase, radius 3, 1px border (`border-strong` neutral; `warn` wash for SAMPLE/SHOW; `accent-wash-18` fill + `accent-border` for active filter).
- **Keyboard hints (J/K/L etc.):** mono 8.5px, `text-muted`, `border-strong` radius-3 chips; toggleable.
- **Waveform:** 2px bars, 1px gap, radius 1; played `accent`, unplayed `wave-unplayed`; playhead 2px `accent`; selected region `accent-wash-16` with 2px `accent` edges.
- **Tabs (REELS/TELEGRAM):** active = `text-primary` + 2px `accent` underline; inactive `text-muted`, hover `text-primary`.
- **Popovers (speaker confirm):** `bg-4`, `border-control`, radius 5, padding 14; header status dot + mono label; CONFIRM/EDIT button pair.
- **Drawer (Renders):** right slide-over 404px, `bg-3`, 1px `border-control` left edge, over `overlay-scrim`; header 48px.
- **Inputs (search):** `border-strong`, radius 4, mono 10px `text-faint` placeholder.
- **Empty state (first-run):** 1px dashed `border-control`, radius 5; title 14px/600; body 11.5px `text-muted`; primary CTA; mono footnote.
- **Top bar:** wordmark = 2×15px `accent` tick + "BLUE SHIFT" 13.5/700 + "STUDIO" 9/500 @ 0.26em `text-muted`; breadcrumb 11px with `text-faintest` ▸ separators; RENDER indicator chip (44×3px mini progress) — active state gets `accent-border` + `accent-wash-12`.
- **Library ID column (human ruling, 2026-07-22):** raw public ids (`ep_…`) are never
  displayed in the UI — they are URL/API material only. The prototype's ID column shows
  editorial episode codes (`EP-2026-0212`); until such codes exist as a real field (M1+),
  the Library table has no ID column.
- **Status bar:** mono 9.5px `text-muted`: `QUEUE n · STORAGE a / b TB · ENGINE nnn MS` · (right) version; live render progress inline when active. Engine labels are always Blueshift-neutral (`ENGINE 412 MS`) — never provider names.
- **Focus:** keyboard focus = 1px `accent-border` ring + `accent-wash-12` fill on the focused control (prototype shows hover only; focus mirrors hover with accent instead of white).

## RTL & Persian content

- Persian content blocks: `dir="rtl"`, right-aligned, `font-fa`. Metadata rows above them stay `dir="ltr"` (timecode left, speaker chip right). Mixed-direction inline strings wrapped in `<bdi>`.
- In LTR table cells (Library titles), Persian text is `dir="rtl"` with `text-align:left` + `<bdi>`.
- ZWNJ is preserved verbatim everywhere (transcript, captions, glossary); glossary flags render as dotted `warn` underlines; fidelity mismatches as `danger` washes + dotted underline.
- Caption preview: centered, `text-caption` on `overlay-caption` box, radius 2–3, `box-decoration-break: clone`, max 2 lines, bottom-positioned with 12% safe area (brand kit: Vazirmatn 600 · 64px @ 1080w).

## Motion

Prototype is static; contract minimums:

- Hover/focus transitions: 120ms ease-out (color/border only).
- Drawer & popover: 200ms ease-out slide/fade; scrim fade 150ms.
- Progress bars / waveform playhead: linear, continuous; no bounce, no spring.
- `prefers-reduced-motion: reduce` → all transitions ≤ 1ms, fades only, no movement.
