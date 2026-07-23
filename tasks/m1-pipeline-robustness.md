# Task: m1-pipeline-robustness — no episode may get stuck in processing

**Milestone:** M1 (AC1 blocker, live incident) · **Type:** backend + deploy · **Slug:** `m1-pipeline-robustness`

## Live incident (2026-07-23, diagnosed from prod logs — researched, cited)

The human's 44-minute master (and the Architect's verification episode) exceeded the
worker Job's task timeout (3600s) on its 1 vCPU / 512 MiB config. Cloud Run killed
attempt 1 mid-ffmpeg; the automatic attempt 2 found `status='processing'`, logged
"episode not claimable; no-op", exited 0 — execution reports success, episodes stuck in
`processing` forever, and `POST /episodes/{id}/retry` rejects non-`failed` rows. Three
defects: (a) under-provisioned/under-timed job, (b) killed attempts leave a permanent
claim, (c) success-reporting no-op masks the failure.

Provider facts (docs.cloud.google.com/run: container-contract, task-timeout, create-jobs):
task timeout applies **per attempt**; on timeout Cloud Run sends **SIGTERM with a 10s
grace** before SIGKILL; `CLOUD_RUN_TASK_ATTEMPT` is provided; timeouts may be up to 168h.
ADR 0002's own math (2h master ≈ 60–120 min on 2 vCPU) already implied 1 vCPU/60 min
could never ingest a real episode.

## Scope

1. **Deploy config (deploy.yml — job deploy flags):** worker job → `--cpu=4 --memory=2Gi
   --task-timeout=4h` (4 vCPU ≈ 3–4× realtime x264: 2 h master ≈ 45–75 min, fits with
   margin; cost per ADR 0002 remains ~cents at PoC volume). Keep `--max-retries=2`.
2. **Graceful shutdown (cmd/worker):** on SIGTERM, cancel the stage context, and within
   the grace window mark the claimed episode `failed` (neutral reason, internal error id,
   server-side log with stage + elapsed), then exit non-zero. ffmpeg child must be killed
   with the context (verify /internal/media wires exec.CommandContext or equivalent).
3. **Claim honesty (store + migration):** additive migration: `episodes.claimed_at
   timestamptz NULL`. Claim sets it; terminal transitions clear or ignore it. The
   "not claimable" no-op must log at WARN (not INFO) including the blocking status —
   a retry attempt observing a claim it cannot take is a signal, not a success.
4. **Stale-claim sweeper (backstop for SIGKILL/OOM/crash):** extend the existing
   app-side sweep goroutine (internal/sweep) with a second query: `status='processing'
   AND (claimed_at IS NULL OR claimed_at < now() - $PROCESSING_TTL)` → set `failed`.
   `PROCESSING_TTL` env, default 5h (> task-timeout + slack). `claimed_at IS NULL`
   covers legacy stuck rows — this automatically unsticks the two current prod episodes
   after deploy, making them retryable via the existing API/UI.
5. **Tests:** DB-backed: claim sets claimed_at; stale gate all cases (fresh processing
   survives, stale processing failed, NULL claimed_at processing failed, ready/failed
   untouched); SIGTERM handler unit (mark-failed path, bounded shutdown); sweep interval
   wiring; no change to abandoned-uploads gate semantics (regression).

## Out of scope

GPU (ADR 0002 revisit trigger unmet); LISTEN/NOTIFY; automatic re-drive of failed
episodes; UI changes (FAILED + RETRY already render).

## Acceptance

- make check green; DB-backed tests run.
- Reviewer verifies: sweep gates exactly as above; SIGTERM path bounded well under 10s
  (single UPDATE + log); worker exit codes honest; deploy flags exactly as specced;
  no vendor terms outside allowed zones.
- Architect (post-deploy, operational): confirms the two stuck prod episodes flip to
  failed via sweeper, then retries them via API and watches them reach ready.

## Evidence

Summary; diffs; test transcript; open questions.
