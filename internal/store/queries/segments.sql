-- name: DeleteEpisodeSegments :exec
-- Remove all of an episode's segments. Paired with InsertSegment inside one
-- transaction, this makes a re-run of the transcribe stage idempotent: the prior
-- transcript is replaced wholesale rather than duplicated. episode_id is the
-- internal id, resolved org-scoped by the caller.
DELETE FROM segments WHERE episode_id = $1;

-- name: InsertSegment :exec
-- Insert one transcript segment. `words` is the positional jsonb array the schema
-- documents ([text, start_ms, end_ms, conf] tuples); text/words are stored
-- verbatim from ASR (no normalization at rest).
INSERT INTO segments (episode_id, idx, start_ms, end_ms, text, words)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListSegmentsByEpisode :many
-- List an episode's transcript in order. episode_id is the internal id, resolved
-- org-scoped by the caller.
SELECT idx, start_ms, end_ms, text, words
FROM segments
WHERE episode_id = $1
ORDER BY idx;
