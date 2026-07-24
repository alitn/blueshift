# Task: m1-pipeline-details — hover card with named stages + per-stage durations

**Milestone:** M1 (human-requested ASAP) · **Type:** full-stack · **Slug:** `m1-pipeline-details`
**Design note:** interim design by the Architect per the human (the claude.ai design agent
re-designs later); use EXISTING conventions only — DESIGN.md popover spec (bg-4,
border-control, radius 5, padding 14, mono labels), status-dot colors from tokens.

## Scope

1. **Timing capture (worker/store, additive):** new table `stage_timings(id, episode_id
   FK, stage text, started_at timestamptz, finished_at timestamptz NULL, outcome text
   NULL CHECK IN ('ok','failed') )` + UNIQUE(episode_id, stage, started_at). Worker
   writes: row at claim (started), finish+outcome at advance/ready/failed — including
   the SIGTERM detached-finalize path (best-effort there; never blocks the 5s bound).
   Idempotent re-runs create new rows (history kept; latest per stage wins for display).
   No backfill (old episodes simply show no durations).
2. **API:** `GET /api/episodes/{id}/pipeline` (auth, org-scoped 404): neutral DTO
   `{stages:[{name, status: done|active|pending|failed|unreached, duration_ms?}],
   queued_ms?, total_ms?}` — stage list derived from the ACTIVE chain the episode ran
   (registry names are already client-visible product terms), statuses from
   status/current_stage/timings, queued_ms = uploaded_at→first ingest start. Fetched
   lazily on hover/focus, cached client-side per episode until status changes.
3. **UI (LibraryTable pipeline cell):** hover OR keyboard-focus opens the popover:
   five rows — stage display name (mono 11px; map diarize→"SPEAKERS", moments→"MOMENTS",
   ingest→"INGEST", transcribe→"TRANSCRIBE"), status dot (done=step-done, active=accent
   w/ subtle pulse, failed=danger, pending/unreached=border-default), right-aligned mono
   duration ("1M 42S") for finished stages; footer row: QUEUED + TOTAL. Loading state =
   skeleton lines; error = neutral. Dismiss on unhover/blur/Escape. Must not interfere
   with row click/open or the remove action. Works on the episode view's bars too IF
   trivially shareable — else Library only (flag).
4. **Tests:** DB-backed timings (claim/finish/failed/re-run history); API (org-scope,
   derivation incl. legacy no-timings episodes, active episode); vitest (popover states,
   mapping, keyboard); e2e (hover shows named stages + durations on the seeded sample;
   axe incl. popover; tokens). Baselines: at-rest UNCHANGED (popover only on hover) —
   verify zero baseline drift (rest-invisible pattern like the remove action). Library
   poll unchanged (no payload bloat — lazy endpoint).

## Acceptance

- make check + e2e functional green; zero baseline drift.
- Reviewer verifies: timing capture doesn't touch the robustness invariants (claim
  atomicity, detached finalize bound), org-scoping, legacy episodes degrade gracefully,
  popover per DESIGN.md conventions, no poll bloat.
- Architect post-deploy: hover the seeded + real episodes; durations match the worker
  logs. Human verifies the hover card.

## Evidence

Summary; diffs; screenshots (hover open); gate transcripts; baseline statement; open questions.
