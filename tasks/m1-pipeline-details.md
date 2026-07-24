# Task: m1-pipeline-details — stage provenance (stage_runs) + hover card

**AMENDED 2026-07-24 (human-directed): the timing table becomes full stage PROVENANCE.**

**Milestone:** M1 (human-requested ASAP) · **Type:** full-stack · **Slug:** `m1-pipeline-details`
**Design note:** interim design by the Architect per the human (the claude.ai design agent
re-designs later); use EXISTING conventions only — DESIGN.md popover spec (bg-4,
border-control, radius 5, padding 14, mono labels), status-dot colors from tokens.

## Scope

1. **Provenance capture (worker/store, additive):** table `stage_runs(id, episode_id FK,
   stage text, started_at, finished_at NULL, outcome text NULL CHECK('ok','failed'),
   engine_label text NULL, engine_detail text NULL, cost_cents int NULL, items_in int
   NULL, items_out int NULL, attempt int NULL, params jsonb NULL)`. Worker writes at
   claim + finalize (incl. best-effort in the SIGTERM detached path). Semantics:
   `engine_label` = the PUBLIC versioned neutral label (bs-asr-2 for the current speech
   engine — BUMPED from bs-asr-1 to mark the provider switch; bs-lm-1 for LLM stages;
   ingest gets bs-media-1). `engine_detail` = PRIVATE provider truth (e.g. the concrete
   model@location) — DB/server only, NEVER in any DTO (Reviewer enforces). cost_cents
   from llm_calls linkage / ASR duration-rate; items_in/out (e.g. segments in→out,
   words); attempt from the billable counter; params only where tunables exist
   (segmentation thresholds). History kept on re-runs; latest-per-stage wins for
   display. No backfill. **Label-versioning rule (document in RUNBOOK proposal):
   engine-behavior changes bump the public label; label→provider mapping is recorded
   here — selective reprocessing becomes `WHERE engine_label = old`.**
2. **API:** `GET /api/episodes/{id}/pipeline` (auth, org-scoped 404): neutral DTO
   `{stages:[{name, status, duration_ms?, engine: <public label>, cost_cents?}], queued_ms?, total_ms?}` (engine_detail NEVER exposed) — stage list derived from the ACTIVE chain the episode ran
   (registry names are already client-visible product terms), statuses from
   status/current_stage/timings, queued_ms = uploaded_at→first ingest start. Fetched
   lazily on hover/focus, cached client-side per episode until status changes.
3. **UI (LibraryTable pipeline cell):** hover OR keyboard-focus opens the popover:
   five rows — stage display name (mono 11px; map diarize→"SPEAKERS", moments→"MOMENTS",
   ingest→"INGEST", transcribe→"TRANSCRIBE"), status dot (done=step-done, active=accent
   w/ subtle pulse, failed=danger, pending/unreached=border-default), right-aligned mono
   duration ("1M 42S") for finished stages, engine label under the stage name (mono
   faint, e.g. "BS·ASR 2"), per-stage cost when known; footer: QUEUED + TOTAL (+ total
   cost — feeds the Library COST column in a later task). Loading state =
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
