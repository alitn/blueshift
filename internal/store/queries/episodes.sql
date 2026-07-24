-- name: InsertEpisode :one
INSERT INTO episodes (
    org_id, show_id, title, source_filename, language, master_object_key, master_size_bytes
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
RETURNING *;

-- name: GetEpisodeByPublicID :one
SELECT * FROM episodes
WHERE public_id = $1
  AND org_id = $2
  AND deleted_at IS NULL;

-- name: ListEpisodesByOrg :many
SELECT * FROM episodes
WHERE org_id = $1
  AND deleted_at IS NULL
ORDER BY created_at DESC, id DESC;

-- name: UpdateEpisodeStatus :one
UPDATE episodes
SET status = $3,
    updated_at = now()
WHERE public_id = $1
  AND org_id = $2
  AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteEpisode :execrows
-- Tenant-facing soft delete (CLAUDE.md soft-delete convention): stamp
-- deleted_at once and keep the row. Every read/claim/finalize/sweep path
-- filters deleted_at IS NULL, so a deleted episode is invisible to the API,
-- unclaimable by pipeline stages, and unbillable — deleting a mid-flight
-- episode cleanly starves its stage chain. Org-scoped: a caller can only ever
-- delete their own org's episode; an unknown/foreign id matches no row (the
-- handler's 404). Idempotent: an already-deleted row still matches (COALESCE
-- preserves the original deleted_at and updated_at is only bumped on the first
-- delete), so a repeated DELETE reports the row again (the handler's 204).
-- Storage objects (master/proxy) are deliberately NOT removed here: soft
-- delete is row-level only, and object GC for deleted episodes is a later,
-- separate concern.
UPDATE episodes
SET deleted_at = COALESCE(deleted_at, now()),
    updated_at = CASE WHEN deleted_at IS NULL THEN now() ELSE updated_at END
WHERE public_id = $1
  AND org_id = $2;

-- name: DeleteOrphanEpisode :execrows
-- Compensating rollback for a create that failed AFTER the row was inserted but
-- BEFORE an upload URL could be minted (e.g. signing unavailable). It hard-deletes
-- the just-created row so a failed create leaves nothing behind. It is narrowly
-- gated — org-scoped, status still 'uploaded', and no master key yet — so it can
-- only ever remove a fresh orphan, never an episode that started uploading or
-- advanced. Returns the affected-row count so a caller can log a no-op.
-- deleted_at IS NULL: a soft-deleted row is a frozen record of a tenant action
-- and no hard-delete path may touch it.
DELETE FROM episodes
WHERE public_id = $1
  AND org_id = $2
  AND status = 'uploaded'
  AND master_object_key IS NULL
  AND deleted_at IS NULL;

-- name: SweepAbandonedEpisodes :execrows
-- System-level TTL sweep of abandoned uploads: a create can succeed
-- server-side and then the CLIENT abandons the upload (CORS failure, closed tab,
-- lost network), leaving a row stuck at 'uploaded' with no master key that no
-- future PUT will ever complete. Across ALL orgs (this is a system maintenance
-- sweep, not a tenant action, so it is deliberately not org-scoped) hard-delete
-- rows older than the TTL whose upload never landed. The gate is the same narrow
-- orphan shape as the create-time rollback (status 'uploaded', no master key)
-- plus an age floor, so it can only ever remove a long-abandoned half-created
-- row — never an episode that started uploading or advanced. Returns the count.
-- deleted_at IS NULL: a soft-deleted row is a frozen record of a tenant action
-- (the user removed the episode); the sweep must neither resurrect nor
-- hard-delete it.
DELETE FROM episodes
WHERE status = 'uploaded'
  AND master_object_key IS NULL
  AND created_at < now() - sqlc.arg(ttl)::interval
  AND deleted_at IS NULL;

-- name: SetEpisodeMasterKey :one
-- Record the verified master object key after the client confirms the upload
-- landed. Org-scoped so a caller can only complete an upload for their own org's
-- episode. Status is left as 'uploaded'; the worker flips it later.
UPDATE episodes
SET master_object_key = $3,
    updated_at = now()
WHERE public_id = $1
  AND org_id = $2
  AND deleted_at IS NULL
RETURNING *;

-- name: RetryFailedEpisode :one
-- State-guarded retry: atomically move a single 'failed' episode back to
-- 'uploaded' so the ingest trigger can re-run it, clearing the prior error_id.
-- Org-scoped and gated on status = 'failed', so a caller can only retry their
-- own org's failed episode and a row in any other state is left untouched
-- (pgx.ErrNoRows, which the handler maps to 409). claimed_at is cleared: the row
-- returns to the unclaimed 'uploaded' state and the next claim stamps it fresh.
UPDATE episodes
SET status = 'uploaded',
    error_id = NULL,
    claimed_at = NULL,
    updated_at = now()
WHERE public_id = $1
  AND org_id = $2
  AND status = 'failed'
  AND deleted_at IS NULL
RETURNING *;

-- name: ClaimEpisodeForStage :one
-- Stage-aware compare-and-set claim: atomically take an episode for a stage,
-- stamp current_stage = the stage, and re-arm claimed_at = now(). Two shapes,
-- selected by prev_stage:
--
--   * Entry stage (prev_stage IS NULL, e.g. ingest): move a single 'uploaded'
--     episode to 'processing'. The status predicate is the concurrency guard — a
--     second concurrent worker finds no matching row and no-ops (pgx.ErrNoRows).
--     Behaviour is identical to the M0 ingest claim, plus setting current_stage.
--
--   * Continuation stage (prev_stage set): the episode is already 'processing',
--     sitting at the predecessor stage (the prior stage's finalize left
--     current_stage there). The guard current_stage = prev_stage is the
--     compare-and-set: the first claim advances current_stage to the new stage,
--     so a duplicate/concurrent claim finds no matching row and no-ops. This is
--     what makes auto-advance loop- and skip-proof: a stage can only be claimed
--     from its immediate predecessor, never from itself or an earlier stage.
--
-- The returned org_id is how the worker scopes every later write to the claimed
-- tenant; it never takes an org from its arguments. claimed_at is the backstop
-- signal the stale-claim sweeper reads to force-fail a 'processing' row whose
-- worker died without finalizing it.
UPDATE episodes
SET status = 'processing',
    current_stage = sqlc.arg(stage),
    claimed_at = now(),
    error_id = NULL,
    updated_at = now()
WHERE public_id = sqlc.arg(public_id)
  AND deleted_at IS NULL
  AND (
        (sqlc.narg(prev_stage)::text IS NULL AND status = 'uploaded')
     OR (sqlc.narg(prev_stage)::text IS NOT NULL
         AND status = 'processing'
         AND current_stage = sqlc.narg(prev_stage))
      )
RETURNING *;

-- name: AdvanceEpisodeStage :one
-- Non-terminal (intermediate) stage finalize: the stage completed but a next
-- stage will run, so the episode STAYS 'processing'. It records the stage's
-- outputs (proxy key + measured duration; a NULL arg leaves the existing value
-- untouched via COALESCE), clears error_id, and RE-ARMS claimed_at = now() so the
-- stale-claim sweep grants the next stage a fresh TTL to start — the handoff
-- window is never left with a NULL claimed_at, which the sweep would treat as a
-- dead legacy claim and force-fail immediately. current_stage is left AT the
-- completing stage on purpose: the next stage's claim advances it (that
-- current_stage transition is the continuation claim's compare-and-set guard).
-- Org-scoped and gated on 'processing' + current_stage = the completing stage, so
-- a lost race, a foreign org, or a stage that no longer matches is an idempotent
-- no-op (pgx.ErrNoRows) — never a cross-tenant or out-of-order write.
UPDATE episodes
SET proxy_object_key = COALESCE(sqlc.narg(proxy_object_key), proxy_object_key),
    duration_ms = COALESCE(sqlc.narg(duration_ms), duration_ms),
    error_id = NULL,
    claimed_at = now(),
    updated_at = now()
WHERE public_id = sqlc.arg(public_id)
  AND org_id = sqlc.arg(org_id)
  AND status = 'processing'
  AND current_stage = sqlc.arg(current_stage)
  AND deleted_at IS NULL
RETURNING *;

-- name: GetEpisodeStatusByPublicID :one
-- Look up an episode's current status by public id alone (not org-scoped, like
-- ClaimEpisodeForStage: the worker has no org until it claims). Used only to
-- annotate the server-side WARN a worker logs when it cannot take a claim — the
-- blocking status is why the claim was refused. Server-log-only, never client
-- surface.
SELECT status FROM episodes
WHERE public_id = $1
  AND deleted_at IS NULL;

-- name: MarkEpisodeReady :one
-- Finalize a successful run: flip to 'ready', preserving the outputs earlier
-- stages recorded. proxy_object_key/duration_ms are COALESCEd (a NULL arg leaves
-- the existing value untouched), so the terminal stage — which today is
-- transcribe, and produces no proxy or duration of its own — does not wipe the
-- proxy key and measured duration ingest recorded. A stage that DOES produce them
-- (a single-stage pipeline where ingest is terminal) still passes non-NULL and
-- sets them, exactly as before. Org-scoped and gated on 'processing' so it only
-- ever completes the run this worker claimed (idempotent no-op otherwise).
-- claimed_at is cleared: the run is done, no claim is in flight.
UPDATE episodes
SET status = 'ready',
    proxy_object_key = COALESCE(sqlc.narg(proxy_object_key), proxy_object_key),
    duration_ms = COALESCE(sqlc.narg(duration_ms), duration_ms),
    error_id = NULL,
    claimed_at = NULL,
    updated_at = now()
WHERE public_id = sqlc.arg(public_id)
  AND org_id = sqlc.arg(org_id)
  AND status = 'processing'
  AND deleted_at IS NULL
RETURNING *;

-- name: MarkEpisodeFailed :one
-- Finalize an exhausted stage: record a neutral error_id and flip to 'failed'.
-- Org-scoped and gated on 'processing' for the same reason as MarkEpisodeReady.
-- claimed_at is cleared: the run is done, no claim is in flight.
UPDATE episodes
SET status = 'failed',
    error_id = $3,
    claimed_at = NULL,
    updated_at = now()
WHERE public_id = $1
  AND org_id = $2
  AND status = 'processing'
  AND deleted_at IS NULL
RETURNING *;

-- name: IncrementEpisodeProcessAttemptsBelowCap :one
-- Cost-safety gate (CLAUDE.md "Billable-service cost safety"): atomically record
-- that a billable stage is about to start a paid engine call, but ONLY while the
-- per-episode attempt count is still below the cap. It increments process_attempts
-- and returns the new value; the CHECK `process_attempts < max_attempts` in the
-- WHERE clause makes it a compare-and-set — at or above the cap NO row matches, so
-- nothing is incremented and the caller gets pgx.ErrNoRows, which the store maps to
-- "not allowed" so the stage refuses to call the engine. Org-scoped like the other
-- finalizers (the stage supplies the org it claimed), so it can never bump another
-- tenant's counter. This is the ONLY writer of process_attempts.
UPDATE episodes
SET process_attempts = process_attempts + 1,
    updated_at = now()
WHERE public_id = sqlc.arg(public_id)
  AND org_id = sqlc.arg(org_id)
  AND deleted_at IS NULL
  AND process_attempts < sqlc.arg(max_attempts)
RETURNING process_attempts;

-- name: SweepStuckProcessingEpisodes :execrows
-- System-level stale-claim sweep: the backstop for a worker that entered
-- 'processing' (Claim) but was SIGKILLed / OOM-killed / crashed before it could
-- finalize the episode ready or failed. Cloud Run reports such an execution
-- "succeeded" (the retry attempt sees 'processing' and cleanly no-ops), so the
-- row would otherwise sit in 'processing' forever and the retry API — which only
-- accepts 'failed' — could never rescue it. Across ALL orgs (system maintenance,
-- deliberately not org-scoped) force-fail rows stuck in 'processing' whose claim
-- is older than the TTL, OR whose claimed_at is NULL. A NULL claimed_at is a
-- legacy claim taken before the claimed_at column existed (the currently-stuck
-- prod episodes): treated as stale so the sweep unsticks them on the first pass.
-- A neutral error_id is recorded (server-side correlation only, never client
-- surface) and claimed_at cleared. Returns the count so the caller can WARN.
UPDATE episodes
SET status = 'failed',
    error_id = sqlc.arg(error_id),
    claimed_at = NULL,
    updated_at = now()
WHERE status = 'processing'
  AND (claimed_at IS NULL OR claimed_at < now() - sqlc.arg(ttl)::interval)
  AND deleted_at IS NULL;
