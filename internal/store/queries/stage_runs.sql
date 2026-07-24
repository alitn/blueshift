-- name: InsertStageRun :one
-- Open a stage-run provenance row at claim time: append-only history, so a
-- re-run/retry INSERTS a new row (never updates the old one) and display reads
-- take the latest row per stage. started_at is the DB clock (DEFAULT now()) —
-- the wall-clock record; duration is derived at read time, never stored.
-- engine_detail is the PRIVATE provider truth and never leaves the server;
-- engine_label is the PUBLIC versioned neutral label.
INSERT INTO stage_runs (episode_id, stage, engine_label, engine_detail)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: FinishStageRun :execrows
-- Close a stage-run row at finalize time: stamp finished_at, the outcome, and
-- whatever the run learned (unit counts, billable attempt, tunables). Gated on
-- finished_at IS NULL so a double finalize (e.g. a lost race) is an idempotent
-- no-op that never rewrites a closed run.
--
-- cost_cents: an explicitly computed cost (the ASR duration-rate) wins; when
-- none is passed, an LLM-backed stage (diarize/moments) links the cost from the
-- llm_calls audit — the sum of the episode's call costs recorded since this
-- run started. SUM over no rows is NULL, so a run with no audited cost stays
-- honestly unknown rather than zero.
UPDATE stage_runs
SET finished_at = now(),
    outcome     = sqlc.arg(outcome),
    cost_cents  = COALESCE(
        sqlc.narg(cost_cents),
        CASE WHEN stage_runs.stage IN ('diarize', 'moments') THEN (
            SELECT SUM(l.cost_cents)::int
            FROM llm_calls l
            WHERE l.episode_id = stage_runs.episode_id
              AND l.created_at >= stage_runs.started_at
        ) END
    ),
    items_in  = sqlc.narg(items_in),
    items_out = sqlc.narg(items_out),
    attempt   = sqlc.narg(attempt),
    params    = sqlc.narg(params)
WHERE stage_runs.id = sqlc.arg(id)
  AND stage_runs.finished_at IS NULL;

-- name: LatestStageRuns :many
-- The display read: the LATEST run per stage for one episode (history rows on
-- re-runs; latest-per-stage wins). engine_detail is deliberately selected too —
-- this query serves the SERVER-side port; the api layer's port type has no
-- field for it, so it can never reach a DTO.
SELECT DISTINCT ON (stage)
    id, episode_id, stage, started_at, finished_at, outcome,
    engine_label, engine_detail, cost_cents, items_in, items_out, attempt, params
FROM stage_runs
WHERE episode_id = $1
ORDER BY stage, started_at DESC, id DESC;
