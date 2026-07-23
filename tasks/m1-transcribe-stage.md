# Task: m1-transcribe-stage — audio → segments with word timings

**Milestone:** M1 · **Type:** backend (worker stage + store) · **Slug:** `m1-transcribe-stage`
**Depends:** m1-stage-machine, m1-asr-impl (both committed first).

## Scope

1. **Segments migration (additive):** `segments` per CLAUDE.md: `id bigint identity,
   episode_id FK, idx int, start_ms int, end_ms int, text text, words jsonb` (array of
   `[w, start_ms, end_ms, conf]`), `created_at timestamptz`; UNIQUE(episode_id, idx);
   pg_trgm GIN index on text. NO speaker_id yet (lands additively with diarize). NO
   embedding column yet (lands with m1-segment-embeddings). sqlc queries: bulk insert
   (idempotent per episode: delete-then-insert within a tx keyed by episode), list by
   episode ordered by idx.
2. **Stage impl:** registered as 'transcribe' in the stage machine. Reads the ingest
   audio object; chunks with ffmpeg per the asr-impl helpers' contract (≤15-min
   segments, boundaries at silence where cheap — document; PlanChunks drives); calls the
   registered engine (label from /internal/lang engine-selection config for
   episodes.language); stitches (StitchTranscripts); Validate(); persists segments in
   one tx. **Verbatim invariant: store ASR text and words EXACTLY as returned — no
   normalization at rest** (normalization is a comparison/caption-time concern via
   /internal/lang). Timestamps only from ASR (chunk offsets are arithmetic on ASR
   values). Glossary bias: fetch glossary_terms for the episode language, pass as bias
   terms. Engine raw metadata blob → server-side audit log only.
3. **Config/wiring:** env→SpeechConfig loader (deferred from m1-asr-impl): region,
   model label mapping, bucket; registry wiring in cmd/worker; RUNBOOK already documents
   the values.
4. **Tests:** DB-backed segments insert/idempotency/ordering; stage test with the fake
   engine (deterministic fixture → exact rows asserted, verbatim bytes incl. ZWNJ);
   chunk-offset arithmetic against stitch fixtures; failure paths (engine error →
   stage failed, neutral); e2e/demo unaffected until stage is enabled — gate the stage
   registration behind the auto-advance flag reality (ingest still terminal for demo
   until moments/render exist; enable transcribe in the chain, terminal after
   transcribe → status ready as the new M1-partial behavior; update e2e expectations
   accordingly with the fake engine in make demo).

## Out of scope

Diarization/speakers; embeddings; moments; transcript UI (next tasks).

## Acceptance

- make check green; make eval green (lang goldens untouched); demo e2e green with the
  fake engine transcribing the seeded episode.
- Reviewer verifies: verbatim-at-rest (byte equality incl. U+200C in a test); additive
  migration; no provider terms outside /internal/asr; idempotent re-run of the stage.

## Evidence

Summary; diffs; test transcript; open questions.
