# Task: m1-transcript-sync — player ↔ transcript two-way sync (human-specified)

**Milestone:** M1 · **Type:** UI · **Slug:** `m1-transcript-sync`
**Human spec (2026-07-24, verbatim intent):** the current segment must be highlighted and
kept in sync with the video both ways; segments are clickable.

## Behavior (the contract)

1. **Video → transcript:** while the video plays OR when the user scrubs the progress
   bar, the segment containing the current time is highlighted, and scrolled into view
   if not visible.
   - Current segment = the one with `start_ms ≤ t < end_ms`. In silence gaps between
     segments, KEEP the previous segment highlighted until the next begins (no flicker,
     no dead zones). Before the first segment: none highlighted.
   - Auto-scroll only when the highlighted segment is outside the transcript viewport;
     smooth scroll; **never fight the user**: if the user scrolled the transcript
     manually in the last ~4s, suspend auto-follow (resume on next segment change after
     idle, or immediately when they click a segment). Document the chosen policy.
2. **Transcript → video:** clicking (or keyboard-activating) a segment highlights it and
   seeks the video to its `start_ms`.
   - If the video was PLAYING → it continues playing from there.
   - If PAUSED/stopped → it seeks only, stays paused. (Play state is preserved exactly.)
   - Segments are focusable and Enter/Space-activatable (keyboard-first + axe).

## Design

- Highlight treatment per design/DESIGN.md: the transcript highlight token family
  (`accent-wash-14` is the DESIGN.md moment-span highlight; use the design's intended
  treatment for an active/current segment — check prototype screen 01/02 for an active
  state; if the design defines none, use `accent-wash-14` + an accent left/right edge
  consistent with RTL, and flag it for DESIGN.md codification). Tokens only.
- RTL: highlight and edge indicators must respect the RTL text block (edge on the
  reading-start side).
- Cursor/affordance: segments read as clickable (per design conventions), without
  turning the transcript into a button-soup — subtle hover treatment from tokens.

## Implementation notes

- ProxyPlayer is currently a bare video element — expose currentTime updates
  (`timeupdate` ~4Hz is sufficient granularity) and an imperative/bound seek that
  preserves play state; TranscriptPane gets the active idx + an onSelect(idx).
  The episode route wires them. No backend changes (start_ms/end_ms already in the DTO).
- Time mapping is a pure function (ms → segment idx, with the gap policy) — unit-test it
  exhaustively (boundaries, gaps, before-first, after-last).

## Tests / DoD (UI task — full DoD)

- vitest: mapping function; click → onSelect wiring; play-state preservation (mock video
  element: playing→seek+still playing; paused→seek+still paused); highlight class
  application; auto-follow suspension logic.
- e2e (demo, fake engine): play → highlight advances across ≥2 segments; scrub → correct
  segment; click a later segment while paused → video currentTime jumps, stays paused;
  click while playing → keeps playing. axe smoke; RTL assertions intact.
- Token conformance: highlight colors from tokens (no raw hex — gate).
- **Baselines:** the at-rest episode view (t=0, paused) — decide + document whether
  segment 0 is highlighted at rest per the gap policy ("before first segment: none")…
  at t=0 the first segment (start_ms=0) IS current → highlighted → episode-linux.png ×2
  WILL drift. STOP and report exactly which baselines change; Architect regenerates.

## Acceptance

- make check + make e2e green (functional specs; stale-baseline visual failures are the
  regen signal). Reviewer verifies both sync directions against this spec's exact
  contract (esp. play-state preservation and the no-fight scroll policy), RTL-correct
  highlight, tokens-only, axe.

## Evidence

Summary; diffs; screenshots (highlighted state); test transcript; baseline-impact; open questions.
