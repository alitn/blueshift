# Task: m1-transcribe-reenable — turn transcribe back on, end-to-end and gated

**Milestone:** M1 · **Type:** backend + deploy + e2e · **Slug:** `m1-transcribe-reenable`
**Depends:** m1-stages-config-gate, m1-e2e-gates-trunk (both committed). **Human decision
required before the prod portion:** whether to run paid Cloud Speech in prod.

## Why

m1-stages-config-gate parked transcribe out of the active chain (ingest-terminal) to
un-break prod + trunk. This task re-enables it correctly, with the gaps that caused the
regression closed first.

## Scope

1. **Demo auto-advance env fix (root cause of the e2e upload timeout):** the seeded
   sample transcribed fine (explicit seed) but an UPLOADED episode timed out because the
   auto-advance-spawned transcribe worker did not run in fake mode in the demo/CI env.
   Diagnose precisely (exec trigger env inheritance vs. app/worker env vs. ENV-derived
   ASRMode default) and fix so that in demo/CI `PIPELINE_STAGES=ingest,transcribe` +
   fake engine makes an UPLOADED episode reach ready at transcribe. Likely the exec
   trigger must carry ASR_ENGINE_MODE (and the demo must set it) through the whole
   ingest→transcribe auto-advance chain.
2. **Enable transcribe in demo/CI:** set `PIPELINE_STAGES=ingest,transcribe` +
   `ASR_ENGINE_MODE=fake` for make demo / e2e. Update `token-conformance.spec.ts` and
   `flow.spec.ts` for the two-stage reality (seeded sample bars 1-2 done; uploaded
   episode reaches READY through both stages). Re-authorize the visual baseline refresh
   (Architect) for the bars-1-2-done Library.
3. **Prod ASR config (GATED on the human Speech decision):** if approved, set the worker
   Job's ASR env in deploy.yml (`ASR_ENGINE_MODE=speech`, region=us-central1, model,
   project, bucket, language map per docs/RUNBOOK.md), enable speech.googleapis.com,
   grant the worker/Speech service agent the bucket read the m1-asr-impl error fixture
   documents, verify the real batch path works against a real prod upload, and set
   `PIPELINE_STAGES=ingest,transcribe` on the prod service+job. If NOT approved, leave
   prod `PIPELINE_STAGES=ingest` and ship only the demo/e2e re-enablement.
4. **The trunk e2e gate (m1-e2e-gates-trunk) must be live first** so this re-enablement
   cannot merge red.

## Acceptance

- make check + make eval + **make e2e** green with transcribe in the demo chain.
- If prod enabled: a real prod upload reaches READY with a real Persian transcript
  (segments persisted verbatim); Architect verifies operationally.
- Reviewer verifies the demo auto-advance fake-env path is actually exercised by e2e
  (not just the seed).

## Evidence

Summary; diffs; make e2e transcript; (if prod) live prod transcript; open questions.
