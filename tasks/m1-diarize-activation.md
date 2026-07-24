# Task: m1-diarize-activation — wire the live LLM client; diarize joins the chain

**Milestone:** M1 · **Type:** backend + deploy + e2e · **Slug:** `m1-diarize-activation`
**Prereqs (met):** diarize stage committed+parked; /internal/llm committed (schema-validated,
audited); cost-safety (skip-if-speakers-assigned + shared attempt cap) wraps the stage;
segmentation live (real segment boundaries exist for grouping).

## Goal

An uploaded episode flows ingest → transcribe → diarize → ready, with speaker keys
(S1/S2/…) persisted and rendered as chips in the transcript view. Fake LLM in demo/CI
(deterministic, free); real engine in prod (~1–2¢/episode).

## Scope

1. **cmd/worker LLM wiring:** construct the llm.Client + diarize.Engine + Speakers store
   ONLY when the active chain includes `diarize` (mirror the ASR conditional — cost-safety
   pattern). Env → llm engine config: follow internal/llm's actual Options/EngineConfig
   seams (labels bs-lm-1; provider/model/endpoint per the committed gemini impl's needs;
   API key/ADC per its auth path). Fail-fast on missing config exactly like the ASR path.
   Demo/CI: `LLM_ENGINE_MODE=fake` (or the seam internal/llm's fake exposes) with a
   committed deterministic grouping fixture for the seeded sample; NO live LLM in
   check/eval/e2e.
2. **Chain:** `PIPELINE_STAGES=ingest,transcribe,diarize` in demo (lib.sh; seed drives the
   sample through diarize too) and prod (deploy.yml worker env). transcribe becomes
   intermediate (auto-advance), diarize terminal → ready.
3. **Model choice (prod):** flash-class engine for bs-lm-1 (cost table in the spec of
   m1-llm-interface: ~1–2¢/episode); the concrete model id is a deploy env value — the
   implementer verifies the CURRENT valid model id against the provider docs (research
   rule) and records it in deploy.yml + RUNBOOK proposal (docs are Architect-applied).
4. **Web:** the transcript view already renders chips when speaker_key is non-null — verify
   via component tests; update e2e expectations (seeded sample now has fake speaker keys).
   BASELINES: episode-linux.png (both viewports) will drift (chips visible) — do NOT touch
   __screenshots__; report exactly which change (Architect regenerates via the temp-branch
   flow). library.png visual is stubbed (unaffected) — verify.
5. **Cost-safety re-proof in the 3-stage chain:** re-drive of a diarized episode bills
   ZERO (skip path); the shared attempt cap covers both billable stages; tests updated to
   the 3-stage reality without weakening.

## Acceptance

- make check + make eval + make e2e green (3-stage flow via fakes; visual failures on the
  2 stale episode baselines are the expected regen signal — report, don't fight).
- Reviewer verifies: LLM client built only when diarize active; no live LLM in any test;
  neutral errors; the fake grouping is deterministic; prod env names match internal/llm's
  config seams exactly; cost-safety unbroken.
- Architect (post-deploy, operational): upload the 3-speaker excerpt (sample0
  23:00–27:00, the human-designated verification window) → READY through all 3 stages →
  transcript view shows multiple speakers' chips (expect ≥2 distinct keys; 3 if the
  grouping resolves all three voices from text alone — record the observed quality
  honestly for the acoustic-diarization consideration later).

## Evidence

Summary; diffs; 3-stage e2e transcript; baseline-impact statement; prod env; open questions.
