-- 0010_moments — additive: the ranked-moment store. One row per LLM-proposed
-- clip-worthy moment of an episode, produced by the moments stage
-- (internal/pipeline -> internal/moments -> the LLM seam). New table only, so
-- this is additive-only and safe on the existing schema (Org -> Show -> Episode
-- -> Moment -> Clip; clips land with the render arc).
--
-- Verbatim invariant (see the repo standing rules — models decide, they never
-- measure): the model proposes a moment ONLY as a segment-idx span plus an
-- English rationale and a quote copied verbatim from the transcript (the engine
-- validates the quote is a contiguous substring of the span's text before
-- anything is persisted). start_ms/end_ms are DERIVED by the stage
-- WORD-ACCURATELY from the quote: it is located in the span's stored word
-- sequence and the times are its first word's start_ms and last word's end_ms
-- — ASR-measured word times only, never model output, never snapped to
-- segment bounds (so moment precision is independent of segment length).
--
--   * rank      — 1 = best. UNIQUE(episode_id, rank) makes the replace-per-episode
--                 re-run of the moments stage idempotent and orders the moment
--                 rail deterministically; its index also serves episode lookups.
--   * start_idx/end_idx — the inclusive segments.idx span the moment covers
--                 (the transcript-span reference; the ms columns above are the
--                 tighter quote-aligned window inside it).
--                 A span reference, not an FK: segments are replaced wholesale
--                 on a re-transcribe, and moments are likewise replaced by the
--                 stage that follows, so row-level FKs would only fight the
--                 replace choreography.
--   * status    — text + CHECK, never a native enum (schema conventions).
--                 'proposed' (the stage's output) -> 'approved'/'dismissed' by a
--                 human in the moment rail; status_changed_at stamps the change
--                 (NULL = never touched by a human).
--
-- Deliberately NOT here yet (each lands additively with its own task):
--   * public_id — moments are not yet an API-exposed entity; the moment-rail
--     task adds the uuidv7 public_id column additively when it exposes them.
--   * embedding — pgvector semantic moment search is the separate
--     m1-segment-embeddings arc.
CREATE TABLE moments (
    id                bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    episode_id        bigint      NOT NULL REFERENCES episodes (id),
    rank              integer     NOT NULL,
    start_idx         integer     NOT NULL,
    end_idx           integer     NOT NULL,
    start_ms          integer     NOT NULL,
    end_ms            integer     NOT NULL,
    rationale_en      text        NOT NULL,
    quote_fa          text        NOT NULL,
    status            text        NOT NULL DEFAULT 'proposed'
        CHECK (status IN ('proposed', 'approved', 'dismissed')),
    status_changed_at timestamptz NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (episode_id, rank)
);
