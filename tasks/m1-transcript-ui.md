# Task: m1-transcript-ui — see an episode's transcript (RTL Persian, verbatim)

**Milestone:** M1 · **Type:** UI · **Slug:** `m1-transcript-ui`
**Transcript vertical slice** (segments-api ✓ → THIS → real-Chirp activation). The human
verifies this against REAL segments once Chirp is on; build + test it now against fixtures.

## Design source (authoritative)

- `design/DESIGN.md` §"RTL & Persian content" + typography/colour/spacing tables:
  transcript body = `font-fa` (Vazirmatn) 14.5px / line-height 2.0, `text-body` colour,
  panel on `bg-3`, header 40–44px "TRANSCRIPT" label (11px 600 0.16em `text-muted`).
- `design/project/Blue Shift Studio.dc.html` screen 01 (Episode default) — the transcript
  pane (search "TRANSCRIPT FA · … WORDS"): a scrollable panel of speaker turns; each turn
  has an LTR metadata row (timecode left, speaker chip right) above the RTL Persian text.
- Tokens ONLY (no raw hex — hex gate). ZWNJ (U+200C) preserved verbatim in the rendered DOM.

## Scope

1. **`TranscriptPane.svelte`** (components/studio): given an episode public id, calls
   `fetchTranscript(id)` (web/src/lib/transcript.ts, committed) and renders:
   - Panel header: "TRANSCRIPT" + a neutral summary ("FA · N WORDS" — language label upper,
     N = total words across segments; NO provider/confidence-from-provider text unless the
     DTO carries it — it doesn't yet, so just language + word count).
   - One block per segment (idx order): metadata row `dir="ltr"` with the timecode
     (mm:ss from start_ms, left) and, when `speaker_key` is non-null, a mono speaker chip
     (right) showing the raw label (S1/S2); then the segment `text` in a `dir="rtl"`,
     right-aligned, `font-fa` block. Wrap mixed-direction inline bits in `<bdi>`.
   - **States:** loading; empty (segments: []) → an "awaiting transcript" placeholder per
     the design's empty conventions (NOT an error); fetch error → a neutral inline error.
   - ZWNJ preserved verbatim (do not normalize in the view — render bytes as received).
2. **Host it in an episode view reachable from the Library.** The app is currently
   Library + dialogs; add the minimal hosting so a user can open a READY episode and see
   its transcript. Preferred: extend the existing episode-open interaction into an Episode
   view showing the proxy player (reuse PlayerDialog's playback) + the TranscriptPane
   beside it (screen-01 layout, scoped to player + transcript). Moments rail / editor are
   LATER slices — do not build them. Implementer may propose route vs expanded-dialog per
   the existing structure; keep it minimal and keyboard-reachable.
3. **UI Definition of Done** (CLAUDE.md): vitest component tests (word count, timecode
   formatting, speaker chip present iff speaker_key, empty/loading/error states, RTL dir +
   ZWNJ preserved in DOM); token-conformance assertion (transcript body font/colour from
   tokens); axe smoke on the view; **visual baselines at 1440×900 + 1280×800** — capture
   evidence to .artifacts/screens/m1-transcript-ui/; if committed baselines would change
   or need creating, STOP and report — baseline creation/update is Architect-authorized.
4. Playwright: the view renders the transcript against `make demo` seeded data. NOTE the
   demo seed is currently ingest-only (no segments), so seed a fixture transcript FOR THE
   E2E/COMPONENT TESTS ONLY (test fixtures are allowed — the human verifies real data
   later). Do not add fake data to the live/product path.

## Out of scope

Moments rail, clip editor, caption preview, corrections/editing, speaker NAMES (raw S1/S2
only); real-Chirp activation (next task).

## Acceptance

- make check green; component + token + axe + RTL/ZWNJ tests pass; screenshot evidence
  captured; baseline changes flagged for Architect authorization (not self-applied).
- Reviewer verifies against design/DESIGN.md + screen 01: RTL Persian body, LTR metadata
  rows, ZWNJ verbatim in DOM, tokens only, neutral (no provider strings), empty state sane.

## Evidence

Summary; diffs; screenshots; test transcript; baseline-impact statement; open questions.
