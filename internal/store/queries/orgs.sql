-- name: GetOrg :one
SELECT * FROM orgs
WHERE id = $1;
