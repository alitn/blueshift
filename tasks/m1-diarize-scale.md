# Task: m1-diarize-scale — range-based diarization output (fixes full-episode failure)

**Milestone:** M1 (prod failure on the human's full episode) · **Type:** backend · **Slug:** `m1-diarize-scale`

## Receipt (2026-07-24)

Full 44-min episode: transcribe+segmentation produced 249 segments / 6,463 words
cleanly; diarize FAILED (validation → retry → hard fail; zero speaker_keys). The output
contract — assign every one of 249 idxs exactly once as a flat list — is brittle at
scale for flash-class models (drops/duplicates a few → strict validator rejects). The
17-segment clip passes; 249 does not. Contract flaw, not model flakiness.

## Fix — turn-range output contract

1. **Schema change (internal/diarize):** LLM output becomes
   `{turns:[{start_idx, end_idx, speaker_key}]}` — contiguous ranges. Validation:
   ranges sorted, non-overlapping, and **tiling exactly 0..n-1** (no gaps); labels
   `^S[1-9][0-9]*$`; ~10x fewer output tokens at scale and the coverage property is
   structurally natural for the model. Invalid → the existing one-retry-then-fail path.
   Convert ranges → per-segment speaker_key at persist (storage/DTO unchanged).
2. **Prompt frame** updated to ask for speaker TURNS (ranges), which is also closer to
   how diarization is naturally expressed.
3. **Fixtures/eval:** fake recording + eval/diarize golden updated to the range shape
   (`-update` authorized as part of this fix — the Architect ratifies at review); ADD a
   large synthetic fixture (~250 segments mirroring the prod shape) proving the
   validator + conversion at scale, and an eval case for it.
4. **Fallback documented, not built:** if ranges still fail at 2h scale (~600+ segments),
   the next step is windowed diarization with overlap continuity — document in a code
   comment; do NOT build now (MVP bias).
5. **Cost-safety unchanged** (same call sites, skip/cap untouched — verify by test).

## Acceptance

- make check + make eval green (updated goldens; large fixture passes).
- Reviewer verifies: tiling validation exact (gap/overlap/unsorted/out-of-range all
  rejected), conversion correct, verbatim untouched (text/words/times never modified),
  no cost-safety regression.
- Architect post-deploy: RETRY the failed full episode (it is `failed` → retry works;
  transcribe skips free) → diarize succeeds at 249 segments → moments runs → the human
  composes prompts on the full episode.

## Evidence

Summary; diffs; large-fixture transcript; gate transcripts; open questions.
