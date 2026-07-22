-- name: GetAuthContextByEmail :one
-- Resolve a seeded user's authentication context by email: their display name,
-- their org (public id + name) and their role in that org. One membership per
-- user in M0, so LIMIT 1 is exact. Soft-deleted users are excluded.
SELECT
    u.email        AS user_email,
    u.display_name AS user_display_name,
    o.public_id    AS org_public_id,
    o.name         AS org_name,
    m.role         AS role
FROM users u
JOIN memberships m ON m.user_id = u.id
JOIN orgs o ON o.id = m.org_id
WHERE u.email = $1
  AND u.deleted_at IS NULL
LIMIT 1;
