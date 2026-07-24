# Task: m1-chirp3-switch — switch the speech engine to chirp_3/us (fix + codify)

**Milestone:** M1 (live incident, engine landscape shift) · **Type:** backend + deploy · **Slug:** `m1-chirp3-switch`

## Live receipts (Architect, 2026-07-24, prod logs + live probes — cite in code)

1. First real prod batch call failed 403: **"Permission denied for project … on model
   chirp_2 locale fa-IR. It is no longer generally available."** chirp_2's Persian is
   closed off for API callers — the engine choice is forced, not preferential.
2. Live chirp_3 probes (sync + batchRecognize on the real prod audio, location `us`,
   fa-IR, enableWordTimeOffsets): **works — 641 words WITH offsets** on the 4-min file.
   (The docs' feature table claiming chirp_3 lacks word timestamps is wrong/stale —
   overturned empirically; the human's challenge was right.)
3. Second prod attempt (after operational env flip to chirp_3/us) failed 400:
   **"Recognizer does not support feature: word_level_confidence"** — chirp_3 rejects
   `features.enable_word_confidence` (chirp_2 accepted it and returned zeros anyway).
4. chirp_3's first word came back with **startOffset ABSENT** (proto3 omits zero
   Durations) — `parseOffsetMs` already maps empty→0 (verified); needs a regression
   fixture so it stays that way.
5. Architect applied operationally to the worker Job (must be codified or the next
   deploy reverts): `ASR_MODEL=chirp_3, ASR_REGION=us, MAX_PROCESS_ATTEMPTS=10`.

## Scope

1. **internal/asr/speech.go:** stop sending `enableWordConfidence` in recognition
   features (both sync-path config if present and batch). Keep parsing `confidence`
   defaulting to 0 when absent. Cite receipt 3 in the comment.
2. **Recorded fixtures:** update/add a batch fixture whose first word has NO
   startOffset (chirp_3 wire shape, receipt 4) and assert it parses to start_ms=0 with
   Validate() green. Keep existing fixtures consistent (no enable_word_confidence in
   recorded requests).
3. **deploy.yml (worker Job env):** `ASR_MODEL=chirp_2`→`chirp_3`, `ASR_REGION=$REGION`→
   `us` (literal — the multiregion serving location, independent of $REGION),
   `MAX_PROCESS_ATTEMPTS=10` added. Comment the chirp_2 receipt (provider names are
   permitted in deploy files).
4. **docs/RUNBOOK.md "Speech engine" section** (task-required content, as in
   m1-asr-impl): model chirp_3, location `us` + endpoint form, fa-IR Preview status
   note, the chirp_2 no-longer-GA receipt, absent-offset semantics.
5. **Tests:** engine unit — the built request contains NO enable_word_confidence;
   absent-startOffset fixture parses (start 0); existing suite green.

## Acceptance

- make check + make eval green (recorded fixtures only; no live calls in CI).
- Reviewer verifies: no enable_word_confidence anywhere in built requests; the
  absent-offset fixture is load-bearing; deploy.yml matches the operational env exactly
  (chirp_3/us/10); RUNBOOK receipts accurate; provider names confined to permitted zones.
- Architect (post-commit, operational): retry the failed prod episode → READY with a
  real Persian transcript in the prod UI.

## Evidence

Summary; diffs; test transcript; open questions.
