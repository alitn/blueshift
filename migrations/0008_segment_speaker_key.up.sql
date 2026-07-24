-- 0008_segment_speaker_key — additive: episode-local speaker attribution on the
-- transcript. The diarize stage (internal/pipeline -> internal/diarize -> the LLM
-- seam) groups an episode's segments into speaker turns and stamps each with an
-- episode-local label (S1, S2, ...). A new NULLABLE column only, so this is
-- additive-only and safe on the existing segments table: every pre-existing row
-- stays valid with speaker_key NULL (= not yet diarized).
--
-- Deliberately NOT here yet (each lands additively with its own task):
--   * NO foreign key and NO speaker_directory linkage — this is a bare
--     episode-local label. Attaching a label to a real, named speaker (with
--     intro-quote / OCR evidence) is the separate m1-speaker-naming task, which
--     adds speaker_directory and the join additively.
--
-- Verbatim invariant (see the repo standing rules — models decide, they never
-- measure): the diarize stage writes ONLY this column. Segment text, words, and
-- every *_ms timing are untouched — the model decides speaker grouping, it never
-- rewrites text or moves a timestamp. The non-empty CHECK rejects a blank label
-- (NULL still means not-yet-diarized); it is a text field + CHECK, never a native
-- enum, and it is an open label space (no fixed value list), per the conventions.
ALTER TABLE segments
    ADD COLUMN speaker_key text
        CHECK (speaker_key IS NULL OR speaker_key <> '');
