-- 0004_llm_calls_status — additive: record the outcome of every audited model
-- call. /internal/llm writes one llm_calls row per provider call (success, a
-- schema-invalid output, or a failed/retried attempt); this column captures
-- which. It is a new NULLABLE column with a CHECK, so it is additive-only and
-- safe on the append-only llm_calls table (NULL satisfies an IN (...) CHECK, so
-- any pre-existing row stays valid). Status field is text + CHECK, never a
-- native enum, per the repo conventions.
--
--   ok      — the call returned and its output passed strict local validation.
--   invalid — the call returned but its output failed schema / strict-decode
--             validation (this is the attempt that triggers the single retry).
--   error   — the upstream call itself failed (transport, non-2xx, or an
--             unparseable response envelope); no usable output was produced.
ALTER TABLE llm_calls
    ADD COLUMN status text
        CHECK (status IN ('ok', 'invalid', 'error'));
