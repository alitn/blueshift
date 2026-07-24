-- name: DeleteEpisodeMoments :exec
-- Remove all of an episode's moments. Paired with InsertMoment inside one
-- transaction, this makes a re-run of the moments stage idempotent: the prior
-- proposal set is replaced wholesale rather than duplicated (mirroring the
-- segments replace choreography). episode_id is the internal id, resolved
-- org-scoped by the caller.
DELETE FROM moments WHERE episode_id = $1;

-- name: InsertMoment :exec
-- Insert one proposed moment. rationale_en/quote_fa are stored verbatim as the
-- validated engine returned them (the quote is a contiguous substring of the
-- span's transcript text — enforced before persist); start_ms/end_ms are the
-- stage-derived WORD-ACCURATE times of the quote's first/last word within the
-- span (ASR word data, never model output, never segment-snapped). status
-- defaults to 'proposed'; only a human status update ever changes it.
INSERT INTO moments (episode_id, rank, start_idx, end_idx, start_ms, end_ms, rationale_en, quote_fa)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListMomentsByEpisode :many
-- List an episode's moments best-first (rank 1 = best). episode_id is the
-- internal id, resolved org-scoped by the caller.
SELECT rank, start_idx, end_idx, start_ms, end_ms, rationale_en, quote_fa, status, status_changed_at
FROM moments
WHERE episode_id = $1
ORDER BY rank;

-- name: CountEpisodeMoments :one
-- Cost-safety idempotency probe for the moments stage: how many moments the
-- episode already has. A non-zero count means the moment proposal already
-- exists, so the stage SKIPS the billable LLM call entirely (never re-bills on a
-- retry/re-drive). episode_id is the internal id, resolved org-scoped by the
-- caller.
SELECT count(*) FROM moments WHERE episode_id = $1;

-- name: TransitionMomentStatus :one
-- Flip one moment's review status, guarded to the legal transitions only:
-- proposed -> approved/dismissed (the review verdicts) and approved/dismissed ->
-- proposed (the undo). Any other combination matches no row, so the caller sees
-- pgx.ErrNoRows and refuses cleanly. status_changed_at stamps the change.
-- episode_id is the internal id, resolved org-scoped by the caller; (episode_id,
-- rank) is the moment's stable natural key (UNIQUE in the schema).
UPDATE moments
SET status = @status, status_changed_at = now()
WHERE episode_id = @episode_id
  AND rank = @rank
  AND (
        (status = 'proposed' AND @status::text IN ('approved', 'dismissed'))
     OR (status IN ('approved', 'dismissed') AND @status::text = 'proposed')
  )
RETURNING id, status;
