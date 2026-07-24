# Task: m1-diarize-stage — text-anchored LLM diarization (speaker turns on segments)

**Milestone:** M1 · **Type:** backend (worker stage + store + llm) · **Slug:** `m1-diarize-stage`
**Depends:** m1-transcribe-stage, m1-llm-interface, m1-stage-machine (all committed).

## Context & invariants

SPEC-M1: "Diarize — LLM diarization via `/internal/llm`, **text-anchored** (anchors into
ASR text; timestamps still come only from ASR)." This stage assigns an episode-local
speaker label to each transcript segment by asking the LLM to group segments into speaker
turns, anchoring its decision to the ASR **text** — never to timestamps and never by
rewriting text. It is PARKED (built + tested, not in the default active chain) alongside
transcribe until `m1-transcribe-reenable`; it runs when `PIPELINE_STAGES` includes it.

Hard invariants:
- **LLMs decide, they never measure** (CLAUDE.md): the LLM only assigns speaker grouping;
  segment `text`, `words`, and all `*_ms` timings are untouched (verbatim at rest).
- Every LLM call goes through `/internal/llm` with a JSON schema (invalid → one retry →
  hard fail) and is audited in `llm_calls`. No provider names outside `/internal/llm`.
- **Anchor-merge stability** is covered by golden tests in `make eval` (CLAUDE.md names
  this explicitly): the same fixture segments must diarize to the same speaker grouping
  deterministically given a recorded LLM response.

## Scope

1. **Additive migration (next free number after 0007):** `segments.speaker_key text NULL`
   — an episode-local diarization label (e.g. `S1`, `S2`); NULL = not yet diarized.
   No FK, no `speaker_directory` linkage yet (that is m1-speaker-naming). sqlc: a bulk
   `SetSegmentSpeakers` update (episode-scoped, one tx, idempotent) + extend the segment
   read to include speaker_key.
2. **Diarize stage** (`internal/pipeline/diarize.go`), registered as `diarize` after
   `transcribe` in the stage REGISTRY (not the default active chain): reads the episode's
   segments (idx-ordered), builds a neutral LLM request (segments as `{idx, text}` list —
   NO timestamps sent, reinforcing text-anchoring), engine label resolved via
   `/internal/lang` LLM engine-selection config for `episodes.language`, target schema =
   an array of `{segment_idx, speaker_key}` (or turn ranges) that MUST reference existing
   segment idxs and cover them without gaps/overlaps. Validate the LLM output against the
   segment set (every idx assigned exactly once; unknown idx → invalid → the /internal/llm
   one-retry-then-fail path). Persist speaker_key per segment in one tx. Idempotent re-run.
3. **Offline test seam for the LLM** (parallels the fake ASR engine): the stage test must
   run with NO live provider — use the existing `/internal/llm` record/replay seam (or a
   small injectable fake `llm.Client` returning a fixture diarization). If a reusable fake
   LLM engine is the clean shape, keep it minimal and consistent with `internal/asr`'s
   fake; flag if it belongs in a shared spot. NO live LLM calls in make check / CI / eval.
4. **Anchor-merge golden test in `make eval`:** a committed fixture (segments + a recorded
   LLM diarization response) → the produced speaker grouping is asserted byte-stable
   against a golden; fails closed on drift; regeneration only via the eval `-update` flag
   (mirror the lang-eval discipline). This is the CLAUDE.md-named diarization golden.
5. **Tests:** DB-backed speaker_key persistence/idempotency/ordering (internal/dbtest
   harness); stage test with the fake/recorded LLM (exact speaker_key assignments; segment
   text/words/timings asserted UNCHANGED — verbatim); invalid-LLM-output (bad idx / gap /
   overlap) → retry → neutral stage-failed; audit row written to llm_calls; vendor-leak
   assertions on errors + that segment DTOs stay neutral.

## Out of scope

Speaker NAMING (real names, intro-quote/OCR evidence, speaker_directory merge = the
separate m1-speaker-naming task); moments; any UI; enabling diarize in the active chain
(that's m1-transcribe-reenable's job, human-gated on prod Speech).

## Acceptance

- make check + make eval green; DB-backed + eval goldens run (not skipped); no live LLM.
- Reviewer verifies: text-anchoring (no timestamps in the LLM request; timings/text
  unchanged in a test), llm_calls audited, one-retry-then-fail on invalid output, additive
  migration, anchor-merge golden fails closed, no provider names outside /internal/llm,
  diarize registered-but-parked (default active chain still ingest-only).

## Evidence

Summary; diffs; make check + make eval transcripts; the recorded LLM fixture list; open questions.
