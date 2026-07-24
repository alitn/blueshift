-- name: DeleteEpisodeSegments :exec
-- Remove all of an episode's segments. Paired with InsertSegment inside one
-- transaction, this makes a re-run of the transcribe stage idempotent: the prior
-- transcript is replaced wholesale rather than duplicated. episode_id is the
-- internal id, resolved org-scoped by the caller.
DELETE FROM segments WHERE episode_id = $1;

-- name: InsertSegment :exec
-- Insert one transcript segment. `words` is the positional jsonb array the schema
-- documents ([text, start_ms, end_ms, conf] tuples); text/words are stored
-- verbatim from ASR (no normalization at rest). speaker_key starts NULL (not yet
-- diarized); the diarize stage sets it later via SetSegmentSpeaker.
INSERT INTO segments (episode_id, idx, start_ms, end_ms, text, words)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: SetSegmentSpeaker :exec
-- Stamp one segment's episode-local diarization label (speaker_key) by
-- (episode_id, idx). The diarize stage calls this for every segment inside one
-- transaction, so a re-run overwrites the prior speaker grouping wholesale
-- (idempotent). episode_id is the internal id, resolved org-scoped by the caller.
-- Only speaker_key changes — text, words, and timings are never touched here
-- (verbatim invariant: the LLM decides grouping, it never rewrites the transcript).
UPDATE segments SET speaker_key = $3 WHERE episode_id = $1 AND idx = $2;

-- name: CountEpisodeSegments :one
-- Cost-safety idempotency probe for the transcribe stage: how many transcript
-- segments the episode already has. A non-zero count means the episode is already
-- transcribed, so the stage SKIPS the billable ASR call entirely (never re-bills on
-- a retry/re-drive). episode_id is the internal id, resolved org-scoped by the
-- caller.
SELECT count(*) FROM segments WHERE episode_id = $1;

-- name: CountEpisodeSegmentsAndSpeakers :one
-- Cost-safety idempotency probe for the diarize stage: the episode's total segment
-- count and how many already carry a speaker_key (count() ignores NULLs, so
-- `diarized` counts only assigned rows). The stage treats "already diarized" as
-- total > 0 AND diarized = total, and then SKIPS the billable LLM call (never
-- re-bills on a retry/re-drive). episode_id is the internal id, resolved org-scoped
-- by the caller.
SELECT count(*) AS total, count(speaker_key) AS diarized
FROM segments WHERE episode_id = $1;

-- name: ListSegmentsByEpisode :many
-- List an episode's transcript in order, including the diarization speaker_key
-- (NULL until the diarize stage runs). episode_id is the internal id, resolved
-- org-scoped by the caller.
SELECT idx, start_ms, end_ms, text, words, speaker_key
FROM segments
WHERE episode_id = $1
ORDER BY idx;
