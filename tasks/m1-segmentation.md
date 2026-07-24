# Task: m1-segmentation — split provider mega-segments into readable timed turns

**Milestone:** M1 (human-verified gap: transcript renders as one unformatted block) · **Type:** backend · **Slug:** `m1-segmentation`

## Problem (prod receipt 2026-07-24)

chirp_3 batch returned a 4-minute file as ONE segment (641 words). The transcript view
therefore shows a single [0:00] row and a wall of text — no visible timestamps, no turn
structure. Diarization is also blocked: text-anchored grouping needs real segment
boundaries to assign speakers meaningfully.

## Invariants

- **Timestamps only from ASR:** every boundary must fall between two words using their
  existing word timings; segment start/end = first/last word's times. No invented times,
  no LLM involvement (deterministic pure function).
- **Verbatim:** words and their timings byte-unchanged; segmentation only regroups them.
  Segment `text` = the words joined with single spaces EXCEPT when the provider segment
  text is retained verbatim for an unsplit segment (document the join rule and its
  interaction with the fidelity principle: the caption checker compares against WORDS,
  which stay verbatim — the segment text is a derived view; state this explicitly).

## Scope

1. **`internal/asr` pure function `Resegment(t Transcript, opts) Transcript`:**
   split any segment exceeding thresholds into smaller ones at the LARGEST word gaps:
   - split at inter-word gaps ≥ `GapMs` (default 700ms — typical pause; cite a source or
     derive from the fixture data and document);
   - enforce `MaxDurationMs` (default 30s) and `MaxWords` (default 60) by splitting at
     the largest available gap within the window (never mid-phrase at a tiny gap when a
     bigger one exists nearby);
   - never produce empty segments; re-index idx sequentially; Validate() green on output.
   Property tests: word multiset+timings preserved exactly (incl. ZWNJ bytes); output
   segments within bounds where achievable; idempotent (resegmenting output = no-op);
   golden test in `eval/` from the REAL 641-word prod wire shape (scrub nothing — it's
   already neutral text; commit a recorded fixture derived from it or a synthetic
   equivalent if the real one can't be committed — prefer real, it's our own content).
2. **Transcribe stage:** apply Resegment after StitchTranscripts, before Validate+persist
   (config: SEGMENT_GAP_MS etc. via env with the defaults; keep defaults in code).
3. **Demo/fake fixtures:** already multi-segment — unaffected; add one fake fixture that
   NEEDS resegmentation to cover the stage path.
4. **Reprocess the human's episode is OUT of code scope** — the Architect will reprocess
   operationally post-deploy so the prod transcript becomes readable.

## Acceptance

- make check + make eval green. Reviewer verifies: pure/deterministic, ASR-only times,
  verbatim words, bounds respected, idempotence, the eval golden fails closed.
- Architect (post-deploy): reprocess the verification episode → the prod transcript view
  shows multiple timed turns.

## Evidence

Summary; diffs; before/after segment counts on the real fixture; test transcript; open questions.
