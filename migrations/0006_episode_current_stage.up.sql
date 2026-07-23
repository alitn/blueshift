-- 0006_episode_current_stage — additive: name the pipeline stage that is running
-- or next while an episode is being processed. A new NULLABLE column, so it is
-- additive-only and safe on the existing episodes table (pre-existing rows are
-- simply NULL — a legacy row that predates the multi-stage pipeline).
--
-- `status` stays the coarse lifecycle (uploaded/processing/ready/failed).
-- `current_stage` refines it while work is in flight: the claim stamps it to the
-- stage it is running; a non-terminal stage's finalize leaves it there and the
-- next stage's claim advances it (that current_stage transition is the
-- compare-and-set guard for continuation claims). On a terminal stage it stays at
-- the last stage that ran, so a 'ready' or 'failed' row records which stage it
-- reached — the Library reads it to light the per-stage bars and to label a
-- failure ("FAILED — INGEST", "FAILED — TRANSCRIBE", …).
--
-- The CHECK lists the full M1 stage set; NULL passes the CHECK (NULL IN (…) is
-- NULL, not false), which is exactly the legacy/unclaimed state. Only `ingest` is
-- wired in the worker registry today; the rest register as they land.
ALTER TABLE episodes
    ADD COLUMN current_stage text
        CHECK (current_stage IN ('ingest', 'transcribe', 'diarize', 'moments', 'render'));
