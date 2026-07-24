# RUNBOOK — operational procedures

## First user in production (manual, no personal data in the repo)

User rows are never seeded by migrations. Dev/demo users come from `fixtures/dev-seed.sql`
(generic `*@blueshift.local` identities, applied only by `make demo`/`make dev`/tests). In
staging/production the first (and any subsequent) user is created manually:

1. Create the account in Identity Platform (console or gcloud) with the person's real email.
   That store is the credential authority; the app database only maps email → org/role.
2. Connect to Cloud SQL (`gcloud sql connect <instance> --user=postgres --database=blueshift`)
   and run — substituting the placeholders at the prompt, never committing them anywhere:

   ```sql
   BEGIN;
   INSERT INTO users (email, display_name)
   VALUES ('<email>', '<display name>')
   ON CONFLICT (email) DO NOTHING;

   INSERT INTO memberships (org_id, user_id, role)
   SELECT o.id, u.id, '<editor|approver>'
   FROM orgs o, users u
   WHERE o.name = 'Blueshift Pilot' AND u.email = '<email>'
   ON CONFLICT (org_id, user_id) DO NOTHING;
   COMMIT;
   ```

3. Verify: sign in through the app (`AUTH_MODE=identity`); `GET /api/auth/me` must return the
   expected role.

Rules: this SQL template stays placeholder-only; real values live only in the production
database. See CLAUDE.md "No personal data in the repo — ever."

## Speech engine (`bs-asr-1`) — enabling and operating

The first ASR engine behind `/internal/asr` calls the managed Speech v2 API
(`batchRecognize`). This is an internal ops section; provider names are allowed here
(CLAUDE.md permits them in internal repo docs). Nothing below is client-visible — externally
the engine is only ever `bs-asr-1`.

### One-time enablement (already done in the prod project)

1. Enable the Speech API on the project:

   ```sh
   gcloud services enable speech.googleapis.com --project="<project>"
   ```

2. Grant the per-product Speech **service agent** read on the media bucket. Batch reads the
   audio object as `service-<project-number>@gcp-sa-speech.iam.gserviceaccount.com` — NOT the
   caller — so without this grant every file comes back `PermissionDenied` (code 7; this is
   the exact per-file error the offline error-mapping test replays):

   ```sh
   gcloud storage buckets add-iam-policy-binding "gs://<media-bucket>" \
     --member="serviceAgent:service-<project-number>@gcp-sa-speech.iam.gserviceaccount.com" \
     --role="roles/storage.objectViewer"
   ```

   The engine requests `inlineResponseConfig`, so results come back inline on the operation
   and **no bucket write grant** is needed.

### Engine configuration (region, model, phrase cap)

The engine is fully specified by `asr.SpeechConfig` (no provider constants in code). The
label→provider binding is data, resolved at wiring time (`cmd/worker` builds the engine
from the `ASR_*` env — see deploy/README.md; the parameters and their operational
defaults are):

| Config field | Purpose | Value / default |
|---|---|---|
| `Model` | provider model | `chirp_3` — forced, not preferential: the first real prod batch on `chirp_2` failed 403 **"Permission denied for project … on model chirp_2 locale fa-IR. It is no longer generally available."** (2026-07-24) — chirp_2's Persian is closed off for API callers. chirp_3 + fa-IR returns word timestamps (live-verified on the real prod audio: 641 words WITH offsets on the 4-min file; the docs' feature table claiming chirp_3 lacks word timestamps is wrong/stale). Note fa-IR on chirp_3 is **Preview** status — expect provider-side movement; the nightly smoke is the drift detector |
| `Region` | provider **serving location** + endpoint | `us` — the multiregion location chirp_3 serves fa-IR from; the endpoint derives as `https://us-speech.googleapis.com` (form: `https://{location}-speech.googleapis.com`). This is a provider location, independent of the GCP **compute** region: deploy sets the literal `us`, never `$REGION` (`us-central1`). Region/endpoint are config, so switching is a config row, not a code change |
| `Project` | project owning the recognizer + bucket | the deploy project |
| `Bucket` | media bucket holding the audio object | the media bucket (`<project>-media`) |
| `LanguageCodes` | BCP-47 content tag → provider code | e.g. `{"fa":"fa-IR"}`; an unmapped tag passes through verbatim |
| `PhraseCap` | max inline glossary bias phrases sent | `500` (conservative bound under the documented inline model-adaptation limit; excess terms dropped in glossary order) |
| `AdaptationEnabled` | send glossary bias terms as an inline phrase set | on; set false via config if a model version rejects the adaptation block |

**chirp_3 wire semantics (prod receipts, 2026-07-24).** Two behaviours the engine
codifies — both hit the first real prod episode:

- **No word confidence, and the flag must not be sent.** chirp_3 rejects
  `features.enable_word_confidence` outright: the second prod attempt failed 400
  **"Recognizer does not support feature: word_level_confidence"**. (chirp_2 accepted
  the flag and returned zeros anyway, so nothing is lost.) The engine sends only
  `enableWordTimeOffsets`; per-word `confidence` is stored as the provider returns it —
  absent parses to `0`, never fabricated.
- **Absent zero offsets.** chirp_3 omits zero-value proto3 Durations, so the FIRST
  word of a transcript arrives with **no `startOffset` key**. The engine parses an
  absent offset as `0 ms` (`start_ms=0`); the regression fixture
  `internal/asr/testdata/speech/batch_op_done_absent_offset.json` pins this.

**Long audio.** The documented batch limit is 1 min–1 hour in general, but only up to
~20 min when word timestamps are enabled. Masters here run 40 min+, so the worker transcribe
stage (later task) cuts the audio into ≤15-min chunks and merges the per-chunk transcripts
with `asr.StitchTranscripts`; the engine itself transcribes one object per call.

### Deliberate reprocess of an episode (billable — read first)

A capped or already-processed episode is skipped by design: the transcribe/diarize
stages call the paid engine only when their output is missing, and a per-episode
`process_attempts` counter (`MAX_PROCESS_ATTEMPTS`, code default 5; the prod worker
Job sets 10) hard-stops runaway re-drives before any paid call. To force a fresh
billable run of one episode:

```
# 1. reset the counter for that episode
UPDATE episodes SET process_attempts = 0 WHERE public_id = '<uuid>';
# 2. run a single Job execution with reprocess on (per-execution only — NEVER a standing default):
PIPELINE_REPROCESS=true worker <episode_public_id> <stage>
```

Never set `PIPELINE_REPROCESS` as a standing worker env — it would re-bill on every
retry/auto-advance. In dev the exec trigger inherits the parent env, so a reprocess
run cascades to auto-advanced child stages (harmless — the attempt cap still bounds
cost); the prod cloudrun trigger uses per-execution env and does not cascade.


## LLM engine (`bs-lm-1`) — enabling and operating

The first LLM engine behind `/internal/llm` powers the diarize stage (text-anchored
speaker grouping). Externally it is only ever `bs-lm-1`; the label→provider binding is
data — `cmd/worker` builds the client from the `LLM_*` env **only when `diarize` is in
`PIPELINE_STAGES`**.

- `LLM_ENGINE_MODE`: `fake` (dev/demo/CI — replays the committed deterministic grouping
  fixture through the real validate/retry/audit loop; refused when ENV is staging/prod)
  | `live`.
- `LLM_PROVIDER` / `LLM_MODEL`: deploy data, never code. Current prod: `gemini` /
  `gemini-3.5-flash` (verified GA flash-class 2026-07-24; `gemini-3-flash` is
  preview-only; `gemini-3.6-flash` GA'd 2026-07-21, regional rollout unconfirmed).
- `LLM_ENDPOINT`: full API base up to the models collection. **Required for Gemini 3.x**
  (served from the global endpoint only — regional hosts 404):
  `https://aiplatform.googleapis.com/v1/projects/<project>/locations/global/publishers/google/models`.
  Unset it (and set `LLM_PROJECT`/`LLM_REGION`) only for a regionally-served model.
- Auth: worker SA default credentials with `roles/aiplatform.user` (deploy/gcloud.sh
  grants it). `LLM_API_KEY` only for the key-authenticated fallback provider.
- `LLM_PRICE_IN_CENTS_PER_MTOK` / `LLM_PRICE_OUT_CENTS_PER_MTOK`: integer cents per 1M
  tokens (prod: 150/900, pricing page 2026-07-24). Both-or-neither; unset ⇒
  `llm_calls.cost_cents` NULL + WARN per call.
- Cost safety: diarize shares `MAX_PROCESS_ATTEMPTS` with transcribe; a re-drive of a
  diarized episode is a free skip; only `PIPELINE_REPROCESS=true` re-bills. Removing
  `diarize` from `PIPELINE_STAGES` is the kill switch — no client is even constructed.
  Expect ~0.5–6¢/episode. Model swap = env-only worker redeploy.
- Persistent dev/demo databases seeded before the 3-stage chain: the old sample stays
  READY-at-transcribe (the known READY-reprocess gap) and never gains speaker chips —
  recreate the demo DB (or delete the sample row) and re-boot to reseed through all
  three stages.

### Nightly live smoke

`internal/asr/speech_live_test.go` is a real end-to-end call, compiled only under
`go test -tags live` and further gated by `ASR_LIVE_SMOKE`. It never runs in `make check`/CI.
To run it (nightly job, or a deliberate manual check):

```sh
ASR_LIVE_SMOKE=1 \
ASR_SMOKE_PROJECT="<project>" ASR_SMOKE_REGION="us" ASR_SMOKE_MODEL="chirp_3" \
ASR_SMOKE_BUCKET="<media-bucket>" ASR_SMOKE_AUDIO_KEY="<org>/<episode>/proxies/audio.flac" \
ASR_SMOKE_LANGUAGE="fa" \
go test -tags live ./internal/asr/ -run TestSpeechLiveSmoke -v
```

Credentials come from ADC; the running identity's Speech service agent must be able to read
`ASR_SMOKE_AUDIO_KEY` (the bucket grant above). With `ASR_LIVE_SMOKE` unset the test skips
cleanly; set but missing a coordinate, it fails loudly so a misconfigured nightly is never a
silent pass. On drift the nightly opens an issue (CLAUDE.md AI-output QA).
