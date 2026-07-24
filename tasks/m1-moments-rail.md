# Task: m1-moments-rail — moments API + the rail UI (Approve / Dismiss)

**Milestone:** M1 · **Type:** full-stack (thin API + UI) · **Slug:** `m1-moments-rail`
**Depends:** m1-moments-stage committed. Fast-tracked: the human verifies moment
selection in the UI today.

## Scope

1. **API (mirror the transcript endpoint patterns):**
   - `GET /api/episodes/{id}/moments` — auth, org-scoped (foreign/unknown → 404),
     rank-ordered, neutral DTO: `{episode_id, moments:[{rank, start_idx, end_idx,
     start_ms, end_ms, rationale_en, quote_fa, status}]}`. Empty → 200 `[]`.
   - `POST /api/episodes/{id}/moments/{rank}/status` body `{status: approved|dismissed|proposed}`
     — org-scoped; invalid transition/unknown rank → 4xx; sets status_changed_at; 200
     with the updated moment. (Approvals audit-trail beyond status_changed_at is a later
     task — document.)
   - Web client `web/src/lib/moments.ts` + unit tests (mirror transcript.ts).
2. **Rail UI on the episode view** per design screens 01/02 (the Moments side panel,
   `bg-3`, panel header "MOMENTS"): ranked cards — rank chip, mm:ss–mm:ss range, EN
   rationale (LTR), FA quote (RTL, `font-fa`, `<bdi>`, ZWNJ verbatim, accent-wash-14
   quote treatment per DESIGN.md) — statuses visually distinct (proposed default;
   approved = accent border/chip; dismissed = muted/faint, sunk to bottom or collapsed
   per design conventions — implementer judges from prototype, flags if design silent).
   - Card click → seek the video to start_ms (reuse the existing sync seek; play-state
     preserved) and highlight the corresponding transcript span (activeIdx already
     follows the playhead — no extra wiring beyond seek).
   - **Approve / Dismiss** buttons per card → status API, optimistic update; also
     **single-key: A approves, D dismisses the focused card** (SPEC-M1's single-key
     approve; cards focusable, keyboard-navigable). Undo = the reverse action (status
     back to proposed) — keep it simple.
   - Empty state: "AWAITING MOMENTS" (mirror transcript's empty pattern). Layout: rail
     beside player+transcript per screen 01 (three-column feel at 1440; sensible at
     1280 — implementer proposes, screenshots decide).
3. **Tests/DoD (full UI DoD):** API DB-backed tests (org-scope, transitions, empty, 404);
   vitest (cards, statuses, key handling, optimistic update, RTL/ZWNJ); e2e (approve via
   keyboard + button, dismiss, click→seek, axe, tokens); screenshots to
   .artifacts/screens/m1-moments-rail/. **Baselines: episode-linux.png ×2 WILL drift**
   (the rail appears) — report, don't touch; library likely NOT (verify).

## Acceptance

- make check + make e2e functional green. Reviewer verifies org-scoping, verbatim quote
  rendering (byte-exact vs API), status transitions, single-key approve, design fidelity
  vs screens 01/02, tokens only, axe.
- Architect post-deploy: fresh 4-stage upload → REAL ranked moments render; approve one
  via keyboard; the human sees moment selection working today.

## Evidence

Summary; diffs; screenshots; gate transcripts; baseline impact; open questions.
