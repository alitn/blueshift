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
