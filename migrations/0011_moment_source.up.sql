-- 0011_moment_source — additive: where a moment came from. 'auto' is the
-- pipeline's ranked proposal set (the moments stage); 'prompt' is a
-- user-composed moment the reviewer chose to keep from a free-prompt compose
-- result. New nullable-equivalent column with a DEFAULT so every existing row
-- backfills to 'auto' — additive-only, no repurpose, no rename.
--
-- Semantics the store enforces around this column:
--   * The moments stage's wholesale replace (reprocess) deletes ONLY
--     source='auto' rows: kept composed moments survive a reprocess.
--   * Kept composed moments are inserted at rank = max(rank)+1 for the
--     episode (the next free rank), so UNIQUE(episode_id, rank) holds and the
--     rail's rank ordering stays deterministic.
--   * Text + CHECK, never a native enum (schema conventions).
ALTER TABLE moments
    ADD COLUMN source text NOT NULL DEFAULT 'auto'
        CHECK (source IN ('auto', 'prompt'));
