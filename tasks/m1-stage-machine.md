# Task: m1-stage-machine — generalize the worker to a multi-stage pipeline

**Milestone:** M1 · **Type:** backend (worker/store) · **Slug:** `m1-stage-machine`

## Context

M0 has one stage (ingest) driving a single status field. M1 adds transcribe → diarize →
moments → render (SPEC-M1). The Library pipeline column already renders five per-stage
bars (m1-pipeline-bars-fix mapped stage 1 only). This task builds the stage machinery;
the stages themselves land in later tasks.

## Scope

1. **Data (additive migration):** `episodes.current_stage text NULL` with CHECK in
   ('ingest','transcribe','diarize','moments','render'); NULL for legacy rows.
   Status stays the coarse lifecycle (uploaded/processing/ready/failed); current_stage
   names what is running/next while status='processing'. Claim semantics
   (claimed_at, stale-sweep, SIGTERM finalize from m1-pipeline-robustness) become
   stage-aware: claim(stage) transitions and re-arms claimed_at per stage.
2. **Worker:** `cmd/worker <episode> <stage>` already exists — generalize dispatch to a
   stage registry (ingest today; later stages register). On stage success, if a next
   stage is registered AND PIPELINE_AUTO_ADVANCE=true (env, default true), the worker
   triggers the next stage execution via the existing trigger mechanism (the worker's SA
   already holds the runner role). Terminal stage → status ready. Failure at any stage →
   failed with the stage recorded (existing FAILED — INGEST label pattern generalizes).
3. **API/DTO:** episode DTO gains neutral `stage` (nullable string) alongside status;
   web maps bars: stages before current → done, current → active/pending per status,
   after → unreached. Library chip text follows the existing pattern ("INGEST…" →
   "TRANSCRIBE…" etc. — labels from a single map, vendor-neutral).
4. **Sweeper:** stale-claim sweep unchanged in gate but failure reason records the stage.
5. **Tests:** stage registry dispatch; auto-advance trigger (fake trigger, both flag
   states); DB-backed stage-aware claim/finalize/sweep; DTO mapping; web mapping tests
   for multi-stage bar states (extend pipeline.test.ts); e2e flow stays green (single
   ingest stage still ends READY as today).

## Out of scope

The transcribe/diarize/moments/render stage implementations; segments schema.

## Acceptance

- make check green; migration additive-only; no behavior change for the deployed
  single-stage pipeline beyond the new nullable DTO field (verify e2e).
- Reviewer verifies: stage-aware claim can't regress m1-pipeline-robustness invariants
  (atomic claim+stamp; detached finalize); auto-advance can't loop or skip; DTO neutral.

## Evidence

Summary; diffs; test transcript; open questions.
