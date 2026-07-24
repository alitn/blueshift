-- 0012_stage_runs — additive: per-stage run provenance. One row per pipeline
-- stage RUN (claim -> finalize), append-only history: a re-run/retry inserts a
-- NEW row rather than updating the old one, and readers take the latest row per
-- stage (started_at DESC). New table only — additive-only, safe on the existing
-- schema. No backfill: episodes processed before this migration simply have no
-- rows here, and every reader degrades to the episode's status/current_stage.
--
-- Timestamps are the record (timestamptz UTC, schema convention): started_at is
-- stamped when the worker claims the stage, finished_at when it finalizes.
-- Duration is DERIVED at read time (finished_at - started_at), never stored.
--
-- Column semantics (written by the worker via internal/store):
--   * outcome       — NULL while the run is in flight; 'ok'/'failed' once
--                     finalized (text + CHECK, never a native enum).
--   * engine_label  — the PUBLIC versioned neutral engine label the stage ran
--                     under (bs-media-1 / bs-asr-2 / bs-lm-1). This is the only
--                     engine field a client surface may ever see. The
--                     label-versioning rule (docs/RUNBOOK.md): engine-behaviour
--                     changes bump the label, so selective reprocessing is
--                     `WHERE engine_label = <old>`.
--   * engine_detail — the PRIVATE provider truth (concrete model@location).
--                     DB/server only: it is NEVER selected into any DTO and the
--                     vendor-leak gate keeps it out of client surfaces. This
--                     migration seeds no data, so no provider name appears here.
--   * cost_cents    — integer cents (money convention). NULL = unknown/free.
--   * items_in/out  — stage-meaningful unit counts (e.g. segments in -> out).
--   * attempt       — the billable counter (episodes.process_attempts) at the
--                     stage's paid call; NULL for non-billable runs.
--   * params        — tunables the run used (e.g. segmentation thresholds),
--                     only where tunables exist.
CREATE TABLE stage_runs (
    id            bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    episode_id    bigint      NOT NULL REFERENCES episodes (id),
    stage         text        NOT NULL
        CHECK (stage IN ('ingest', 'transcribe', 'diarize', 'moments', 'render')),
    started_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz,
    outcome       text        CHECK (outcome IN ('ok', 'failed')),
    engine_label  text,
    engine_detail text,
    cost_cents    integer,
    items_in      integer,
    items_out     integer,
    attempt       integer,
    params        jsonb
);

-- Latest-run-per-stage reads (DISTINCT ON (stage) ... ORDER BY started_at DESC)
-- and the per-episode fetch both walk this index.
CREATE INDEX stage_runs_episode_stage_started_idx
    ON stage_runs (episode_id, stage, started_at DESC);
