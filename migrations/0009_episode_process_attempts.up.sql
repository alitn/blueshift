-- 0009_episode_process_attempts — additive: a per-episode ceiling on how many
-- times a BILLABLE pipeline stage (transcribe, diarize) may start a paid engine
-- call, so a bug or an unforeseen re-drive loop can never rack up unbounded metered
-- cost (see the repo's billable-service cost-safety standing rule). A new NOT NULL
-- column with a DEFAULT, so it is additive-only and safe on the existing episodes
-- table: every pre-existing row backfills to 0.
--
-- Semantics (enforced in code, not here — the DB only stores and floors the
-- count): a billable stage atomically increments this counter immediately BEFORE
-- it calls the engine, but only while it is below the configured cap
-- (MAX_PROCESS_ATTEMPTS, default 5); at or above the cap the stage refuses to call
-- the engine and hard-fails with a neutral error. The counter is shared across the
-- billable stages, so it bounds the TOTAL number of paid engine calls an episode
-- can ever trigger — the idempotency guards (segments exist / speaker_keys set)
-- stop re-billing on success, and this counter stops re-billing on repeated
-- FAILURE. It is deliberately NOT reset by a plain retry/re-drive (that would
-- defeat the ceiling); a deliberate reprocess resets it per docs/RUNBOOK.md.
--
-- It is a plain monotonic counter (text/enum conventions do not apply — this is a
-- count, not a status); the CHECK only rejects a negative value.
ALTER TABLE episodes
    ADD COLUMN process_attempts integer NOT NULL DEFAULT 0
        CHECK (process_attempts >= 0);
