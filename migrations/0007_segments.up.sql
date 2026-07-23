-- 0007_segments — additive: the transcript store. One row per contiguous,
-- speaker-agnostic utterance produced by the transcribe stage (internal/asr ->
-- internal/pipeline). New table only, so this is additive-only and safe on the
-- existing schema.
--
-- Verbatim invariant (see the repo standing rules): `text` and `words` are stored
-- EXACTLY as the ASR engine returned them — no normalization at rest
-- (normalization is a comparison/caption-time concern resolved through
-- /internal/lang). `words` is the positional jsonb array the domain model
-- documents: an array of [text, start_ms, end_ms, conf] tuples, in word order.
--
-- Deliberately NOT here yet (each lands additively with its own task):
--   * speaker_id  — attribution is a separate diarize stage (m1-diarize).
--   * embedding   — pgvector semantic search column (m1-segment-embeddings).
--
-- Timings are integer milliseconds (the schema convention shared with the asr
-- boundary), never floats-of-seconds. `idx` is the 0-based ordinal within the
-- episode; UNIQUE(episode_id, idx) makes the delete-then-insert re-run of the
-- transcribe stage idempotent and orders the transcript deterministically. The
-- pg_trgm extension is enabled in 0001; the GIN trigram index on `text` backs the
-- later semantic/keyword moment search over transcripts.
CREATE TABLE segments (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    episode_id bigint      NOT NULL REFERENCES episodes (id),
    idx        integer     NOT NULL,
    start_ms   integer     NOT NULL,
    end_ms     integer     NOT NULL,
    text       text        NOT NULL,
    words      jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (episode_id, idx)
);
CREATE INDEX segments_episode_id_idx ON segments (episode_id);
CREATE INDEX segments_text_trgm_idx ON segments USING gin (text gin_trgm_ops);
