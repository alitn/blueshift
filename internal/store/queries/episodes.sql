-- name: InsertEpisode :one
INSERT INTO episodes (
    org_id, show_id, title, source_filename, language, master_object_key
) VALUES (
    $1, $2, $3, $4, $5, $6
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
