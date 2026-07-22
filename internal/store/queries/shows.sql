-- name: GetDefaultShowForOrg :one
-- The org's canonical show. Setup auto-creates exactly one show per org in M0,
-- so the lowest-id non-deleted show is that show. Every episode hangs off it
-- until per-show organization arrives.
SELECT * FROM shows
WHERE org_id = $1
  AND deleted_at IS NULL
ORDER BY id
LIMIT 1;
