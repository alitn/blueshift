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
RETURNING *;

-- name: DeleteOrphanEpisode :execrows
-- Compensating rollback for a create that failed AFTER the row was inserted but
-- BEFORE an upload URL could be minted (e.g. signing unavailable). It hard-deletes
-- the just-created row so a failed create leaves nothing behind. It is narrowly
-- gated — org-scoped, status still 'uploaded', and no master key yet — so it can
-- only ever remove a fresh orphan, never an episode that started uploading or
-- advanced. Returns the affected-row count so a caller can log a no-op.
DELETE FROM episodes
WHERE public_id = $1
  AND org_id = $2
  AND status = 'uploaded'
  AND master_object_key IS NULL;

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
DELETE FROM episodes
WHERE status = 'uploaded'
  AND master_object_key IS NULL
  AND created_at < now() - sqlc.arg(ttl)::interval;

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
-- (pgx.ErrNoRows, which the handler maps to 409).
UPDATE episodes
SET status = 'uploaded',
    error_id = NULL,
    updated_at = now()
WHERE public_id = $1
  AND org_id = $2
  AND status = 'failed'
  AND deleted_at IS NULL
RETURNING *;

-- name: ClaimEpisodeForIngest :one
-- Compare-and-set claim: atomically move a single 'uploaded' episode to
-- 'processing'. The status predicate is the concurrency guard — a second
-- concurrent worker finds no matching row and no-ops (pgx.ErrNoRows). The
-- returned org_id is how the worker scopes every later write to the claimed
-- tenant; it never takes an org from its arguments.
UPDATE episodes
SET status = 'processing',
    error_id = NULL,
    updated_at = now()
WHERE public_id = $1
  AND status = 'uploaded'
  AND deleted_at IS NULL
RETURNING *;

-- name: MarkEpisodeReady :one
-- Finalize a successful stage: record the proxy key and measured duration and
-- flip to 'ready'. Org-scoped and gated on 'processing' so it only ever
-- completes the run this worker claimed (idempotent no-op otherwise).
UPDATE episodes
SET status = 'ready',
    proxy_object_key = $3,
    duration_ms = $4,
    error_id = NULL,
    updated_at = now()
WHERE public_id = $1
  AND org_id = $2
  AND status = 'processing'
  AND deleted_at IS NULL
RETURNING *;

-- name: MarkEpisodeFailed :one
-- Finalize an exhausted stage: record a neutral error_id and flip to 'failed'.
-- Org-scoped and gated on 'processing' for the same reason as MarkEpisodeReady.
UPDATE episodes
SET status = 'failed',
    error_id = $3,
    updated_at = now()
WHERE public_id = $1
  AND org_id = $2
  AND status = 'processing'
  AND deleted_at IS NULL
RETURNING *;
