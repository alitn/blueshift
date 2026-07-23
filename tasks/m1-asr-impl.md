# Task: m1-asr-impl — first ASR engine behind /internal/asr

**Milestone:** M1 · **Type:** backend (provider impl) · **Slug:** `m1-asr-impl`
**Depends:** m1-asr-interface (must be committed first).

## Researched + empirically verified facts (Architect, 2026-07-23 — cite in code)

- Persian (fa-IR) transcription is served by `chirp_2` ONLY in region **asia-southeast1**
  (docs: speech-to-text/docs/speech-to-text-supported-languages). chirp_3 does NOT
  support word-level timestamps at all and has fa only in preview — rejected.
- **Live-verified by the Architect** (sync `recognizers/_:recognize`, real Persian
  broadcast audio, 30 s): `chirp_2` + `languageCodes:["fa-IR"]` +
  `features.enableWordTimeOffsets:true` returns full Persian transcript with per-word
  `startOffset`/`endOffset` (82 words, monotonic, sane). Word `confidence` came back 0 —
  investigate `enableWordConfidence`; if the model returns no real confidence, store 0
  and document it.
- Endpoint: `asia-southeast1-speech.googleapis.com`, API v2, implicit recognizer `_`
  with inline config. Batch: `batchRecognize` takes GCS URIs (audio must be readable by
  the caller SA), output inline or to GCS. Batch supports 1 min–8 h audio **BUT** docs
  indicate a ~20-min cap when word timestamps are enabled (stated for chirp_3; MUST be
  verified for chirp_2 — see required experiment).
- Cross-region note: masters live in us-central1; processing in asia-southeast1 is
  accepted for PoC (egress cents; revisit with an ADR if volume grows).

## Scope

1. **Engine impl** in /internal/asr (provider names allowed only here): implements the
   m1-asr-interface Engine contract using Speech v2 REST, auth via existing oauth2/ADC
   plumbing (mirror /internal/llm adc pattern). Region/endpoint/model from engine config
   (registry label `bs-asr-1` → this impl). Offsets converted to ms ints at the boundary.
   Glossary bias terms → v2 adaptation phrase set (inline, up to the documented phrase
   cap; cite). Neutral error mapping + internal error IDs (boundary pattern).
2. **Long-audio strategy — REQUIRED EXPERIMENT FIRST:** run one real `batchRecognize`
   with word timestamps on a >20-min Persian audio (extract from the committed local
   44-min fixture master; upload to a scratch object in the dev bucket; delete after).
   If >20 min works: single batch call per episode. If capped: implement deterministic
   chunking (ffmpeg segment ≤15 min at silence-adjacent boundaries, sequential offsets,
   stitch with monotonic-offset adjustment; document overlap handling). Record the
   experiment's raw evidence in the report; the chosen path must cite it.
3. **Record/replay tests:** fixtures recorded from the real API (scrub project ids);
   word-offset→ms conversion, stitching (if implemented), adaptation payload shape,
   error mapping, Transcript.Validate() passes on real recorded output. NO live calls
   in make check/CI. One `-tags live` smoke test (skipped by default) for the nightly.
4. **Config/RUNBOOK:** engine config rows/env documented (region, model, phrase cap);
   RUNBOOK entry for enabling speech.googleapis.com (already enabled in prod project).

## Out of scope

Worker transcribe stage + persistence (next task); diarization; embeddings.

## Acceptance

- make check green; recorded fixtures only in CI.
- Reviewer verifies: no provider strings outside /internal/asr; ms-int conversion exact
  (rounding documented); the long-audio decision is backed by the recorded experiment;
  Validate() green on real fixture output.

## Evidence

Summary; diffs; experiment transcript + raw API evidence; fixture list; open questions.
