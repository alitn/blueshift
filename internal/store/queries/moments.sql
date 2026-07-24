-- name: DeleteAutoMoments :exec
-- Remove all of an episode's PIPELINE-PROPOSED moments (source='auto').
-- Paired with InsertMoment inside one transaction, this makes a re-run of the
-- moments stage idempotent: the prior proposal set is replaced wholesale
-- rather than duplicated (mirroring the segments replace choreography).
-- Kept user-composed moments (source='prompt') deliberately survive the
-- replace — a reprocess never discards a human's kept composition. episode_id
-- is the internal id, resolved org-scoped by the caller.
DELETE FROM moments WHERE episode_id = $1 AND source = 'auto';

-- name: InsertMoment :exec
-- Insert one stage-proposed moment (source defaults to 'auto'). rationale_en/
-- quote_fa are stored verbatim as the validated engine returned them (the
-- quote is a contiguous substring of the span's transcript text — enforced
-- before persist); start_ms/end_ms are the stage-derived WORD-ACCURATE times
-- of the quote's first/last word within the span (ASR word data, never model
-- output, never segment-snapped). status defaults to 'proposed'; only a human
-- status update ever changes it.
INSERT INTO moments (episode_id, rank, start_idx, end_idx, start_ms, end_ms, rationale_en, quote_fa)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: InsertPromptMoment :one
-- Persist one KEPT user-composed moment (approve-to-keep) at the episode's
-- next free rank — rank = max(rank)+1 computed atomically in the same
-- statement, so UNIQUE(episode_id, rank) holds without the caller reading
-- first (a concurrent keep loses the race as a unique violation the caller
-- retries). The row lands source='prompt' and status='approved' with
-- status_changed_at stamped: keeping IS the human's approval verdict. Texts
-- and times carry the same verbatim/word-accurate contract as InsertMoment —
-- the caller re-validated the quote and re-derived the times against the
-- CURRENT transcript before persisting. episode_id is the internal id,
-- resolved org-scoped by the caller.
INSERT INTO moments (episode_id, rank, start_idx, end_idx, start_ms, end_ms, rationale_en, quote_fa, status, status_changed_at, source)
SELECT @episode_id, COALESCE(MAX(rank), 0) + 1, @start_idx, @end_idx, @start_ms, @end_ms, @rationale_en, @quote_fa, 'approved', now(), 'prompt'
FROM moments
WHERE episode_id = @episode_id
RETURNING rank, start_idx, end_idx, start_ms, end_ms, rationale_en, quote_fa, status, status_changed_at;

-- name: ListPromptMomentIDs :many
-- The episode's kept composed moments (source='prompt') in rank order — the
-- rows the stage replace must renumber to follow a fresh auto set (see
-- ReplaceMoments: the new auto ranks are 1..n, composed rows continue n+1..).
-- episode_id is the internal id, resolved org-scoped by the caller.
SELECT id FROM moments WHERE episode_id = $1 AND source = 'prompt' ORDER BY rank;

-- name: SetMomentRank :exec
-- Renumber one moment (by internal id) during the replace choreography. Only
-- ever called inside the ReplaceMoments transaction; rank is otherwise
-- immutable.
UPDATE moments SET rank = $2 WHERE id = $1;

-- name: ListMomentsByEpisode :many
-- List an episode's moments best-first (rank 1 = best; kept composed moments
-- rank after the auto set by construction). episode_id is the internal id,
-- resolved org-scoped by the caller.
SELECT rank, start_idx, end_idx, start_ms, end_ms, rationale_en, quote_fa, status, status_changed_at
FROM moments
WHERE episode_id = $1
ORDER BY rank;

-- name: CountAutoMoments :one
-- Cost-safety idempotency probe for the moments stage: how many PIPELINE
-- (source='auto') moments the episode already has. A non-zero count means the
-- stage's proposal set already exists, so the stage SKIPS the billable LLM
-- call entirely (never re-bills on a retry/re-drive). Scoped to 'auto'
-- deliberately: a kept user-composed moment is NOT the stage's output and
-- must never suppress the stage's own proposal run. episode_id is the
-- internal id, resolved org-scoped by the caller.
SELECT count(*) FROM moments WHERE episode_id = $1 AND source = 'auto';

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
