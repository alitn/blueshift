-- name: GetMembershipRole :one
SELECT role FROM memberships
WHERE org_id = $1
  AND user_id = $2;
