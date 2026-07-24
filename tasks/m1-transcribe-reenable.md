# Task: m1-transcribe-reenable — turn transcribe ON (real Chirp in prod, fake in demo/e2e)

**Milestone:** M1 · **Type:** backend + deploy + e2e · **Slug:** `m1-transcribe-reenable`
**Prereqs (ALL MET):** cost-safety committed (4d4aa6d) ✓; GCP budget alert set by human ✓;
transcript view live (segments-api + transcript-ui pushed) ✓. Real Chirp is unblocked.

## Goal

Make an uploaded episode actually get transcribed: fake engine in demo/CI (deterministic,
no cost — automated coverage), REAL Chirp in prod (the human verifies real transcripts).
This also fixes the demo auto-advance env bug that caused the original regression's e2e
failure, and adds the two-stage e2e coverage that would have caught it.

## Scope

1. **Demo/CI auto-advance env fix (root cause — diagnose first):** the seeded sample
   transcribed fine (explicit seed) but an UPLOADED episode timed out because the
   auto-advance-spawned transcribe worker did not run in fake mode in demo/CI. The exec
   trigger (WORKER_TRIGGER=exec) spawns the next-stage worker — ensure ASR_MODE (and any
   ASR_* the fake path needs) propagates through the whole ingest→transcribe auto-advance
   chain in demo/CI. Likely the exec trigger must carry/inherit ASR_MODE=fake and the
   demo must export it so the spawned child sees it. Prove with an e2e run where an
   UPLOADED episode reaches ready at transcribe via the fake engine.
2. **Enable transcribe in demo/CI chain:** set `PIPELINE_STAGES=ingest,transcribe` +
   `ASR_MODE=fake` for `make demo` / e2e. Seed the sample through transcribe too (fake) so
   the seeded READY sample has segments (its transcript renders in the view). Update
   `flow.spec.ts` (upload→READY now traverses two stages) and any two-stage expectations.
3. **Real Chirp in PROD (billable — guardrails + budget alert are in place):** in
   `deploy.yml`, the worker Job gets `ASR_MODE=speech` + `ASR_REGION=us-central1` +
   `ASR_MODEL`/`ASR_PROJECT`/`ASR_BUCKET`/`ASR_LANGUAGE_CODES` per docs/RUNBOOK.md +
   `internal/config`, AND `PIPELINE_STAGES=ingest,transcribe`. Verify `cmd/worker`
   `buildASRRegistry` constructs the real SpeechEngine (from m1-asr-impl) when
   ASR_MODE=speech, and the cost-safety conditional (build ASR only when transcribe is in
   the active chain) still holds. Confirm the Speech API is enabled + the Speech service
   agent has bucket read (from m1-asr-impl setup — verify, don't assume).
4. **Cost-safety intact:** transcribe's idempotent skip-if-segments-exist + the per-episode
   attempt cap must remain wired (they are, from m1-cost-safety) — a re-drive of a
   transcribed episode must still bill zero. Add/keep a test asserting it in the two-stage
   flow.
5. **Baseline impact:** enabling transcribe in demo changes the seeded sample's rendering
   (library pipeline bars 1–2 done; episode view shows a populated transcript). The
   library-linux.png and episode-linux.png baselines WILL change — STOP and report which;
   the Architect authorizes + regenerates them (do NOT self-update __screenshots__).

## Acceptance

- make check + make eval + **make e2e** green with transcribe in the demo chain (two-stage
  flow: upload → transcribe(fake) → ready with segments; transcript view shows them).
- Reviewer verifies: the auto-advance fake-env path is exercised by e2e (not just the
  seed); prod deploy config sets ASR_MODE=speech + PIPELINE_STAGES=ingest,transcribe; the
  cost-safety guards still wrap the (now-live) billable call; no provider names outside
  /internal/asr.
- Architect (post-deploy, operational): reprocess the existing prod episode through real
  Chirp → a REAL Persian transcript renders in the prod transcript view. THIS is the
  human's verification artifact.

## Evidence

Summary; diffs; make e2e transcript (two-stage); baseline-impact statement; prod-config
diff; open questions.
