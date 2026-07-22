-- name: GetOrg :one
SELECT * FROM orgs
WHERE id = $1;

-- name: GetOrgByPublicID :one
-- Resolve an org's internal row from the public id carried in the session
-- principal. This is how every org-scoped write turns the caller's org identity
-- (never client input) into the internal org_id used by the queries below.
SELECT * FROM orgs
WHERE public_id = $1;
