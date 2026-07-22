-- name: GetConfig :one
-- Resolve a config key for an org: an org-specific row wins, otherwise the
-- global (NULL org_id) default. NULLS LAST orders the concrete org row ahead of
-- the global fallback.
SELECT value FROM config
WHERE key = $1
  AND (org_id = $2 OR org_id IS NULL)
ORDER BY org_id NULLS LAST
LIMIT 1;
