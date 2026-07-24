# Task: m1-reprocess-api — re-enter a READY episode into the pipeline (safe by skips)

**Milestone:** M1 (human hit the gap 3×) · **Type:** full-stack small · **Slug:** `m1-reprocess-api`

## Why safe & cheap

Cost-safety already makes every billable stage skip when its output exists. Re-entering
an old episode therefore only RUNS (and bills) the stages whose outputs are missing —
e.g. a pre-transcription READY episode re-runs ingest (remux, free-ish) then transcribe/
diarize/moments; an episode processed by an old engine label re-runs nothing unless
PIPELINE_REPROCESS is used deliberately (that stays the operator-only RUNBOOK path).

## Scope

1. **API:** `POST /api/episodes/{id}/reprocess` — auth, org-scoped 404; legal ONLY from
   `ready` or `failed` (processing/uploaded → 409). Transition: status→'uploaded',
   current_stage→NULL, claimed_at→NULL (single org-scoped UPDATE mirroring
   RetryFailedEpisode), then best-effort trigger of ingest (same pattern as retry).
   Does NOT touch process_attempts (the cap still bounds total billables; a capped
   episode 409s with a neutral message at the stage — document).
2. **UI:** Library row action "REPROCESS" for READY episodes (rest-invisible pattern
   like remove; confirm dialog: neutral copy stating only missing steps will run).
   FAILED rows keep RETRY (same semantics, existing).
3. **Tests:** DB-backed transition legality (ready→uploaded; processing 409; foreign
   404); the skip-fast-forward proven end-to-end with the fake engines (an episode with
   segments+speakers but no moments re-enters → ingest runs, transcribe/diarize skip
   bill-zero, moments runs); e2e row action; baselines unaffected (rest-invisible).

## Acceptance

- make check + e2e functional green; zero baseline drift.
- Reviewer verifies: transition guard exact, cost-safety skips proven in-chain, org
  scoping, no interaction with the stale-claim sweeper mid-reprocess.
- Architect post-deploy: reprocess the human's ORIGINAL sample0 row → it fast-forwards
  through the chain, gains transcript/speakers/moments without a duplicate row.

## Evidence

Summary; diffs; skip-fast-forward transcript; open questions.
