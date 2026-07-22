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
