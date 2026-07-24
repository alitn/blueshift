# Task queue

> **RESUME HERE (2026-07-24 late).** Milestone: M1. **LIVE IN PROD:** full 4-stage
> cost-guarded pipeline (ingest→transcribe[chirp_3/us]→diarize[gemini-3.5-flash/global]→
> moments); episode view (RTL transcript, speaker chips, two-way sync, delete); moments
> rail (ranked, verbatim, word-accurate, A/D keys); **free-prompt compose**
> (m1-prompt-moments, live-verified on the 3-speaker clip). Library: human's original
> 44-min sample0 (READY, pre-pipeline), 3-speaker diarized clip, full-pipeline moments
> clip, + "sample0 — full episode" **failed@diarize (249 segments — the flat per-idx
> contract broke at scale)**.
> **IN FLIGHT:** two commits UNPUSHED (`8cc7318` cache-headers = normal-refresh fix;
> `6c3e05b` provenance = stage_runs + hover card + bs-asr-2 label bump);
> **m1-diarize-scale** (range-turn contract fix) built green, IN REVIEW. Plan: on APPROVE
> commit → push all three → one deploy → **RETRY the failed full episode** (transcribe
> skips free) → moments on the 44-min → human composes on it.
> **THEN:** m1-reprocess-api (spec-ready) → m1-youtube-ingest (ADR 0003 accepted;
> youtubedr; spec-ready) → editor-trim → captions → fidelity → render (golden-path
> finish). **HUMAN ACTION PENDING: `gcloud auth login`** (24h org expiry — blocks
> Architect prod-log reads; deploys unaffected, CI has WIF).
> Deferred: speaker-naming (evidence-gated), shots/reframe, deleted-gc, trigger-test
> flakes, ASR_PRICE_CENTS_PER_HOUR unset (cost shows —), m2-signals (to-be-confirmed).
> To resume: read this block + the `## Log` (newest at bottom).

Single source of truth for task state. Only the Architect edits this file. One task = one
spec file `tasks/<slug>.md` = one Implementer dispatch = one Reviewer verdict = one commit.

**States:** `queued → spec-ready → in-progress → in-review → approved → committed`
(plus `blocked(reason)` / `rejected(n)` for review cycles; 3 rejections escalate to human).

## M0 — Walking skeleton

| Slug | Task | State | Spec |
|------|------|-------|------|
| m0-scaffold | Repo scaffold, gates, docs (this) | committed (human-approved) | docs/SPEC-M0.md |
| m0-design-contract | DESIGN.md transcribed from design export (Architect) | committed | design/DESIGN.md |
| m0-go-skeleton | app server, health, config, embed | committed | tasks/m0-go-skeleton.md |
| m0-db-baseline | migration 0001 + sqlc + ids codec | committed | tasks/m0-db-baseline.md |
| m0-check-hardening | make check fails on any red step | committed | tasks/m0-check-hardening.md |
| m0-web-skeleton | SvelteKit + tokens + ui primitives | committed | tasks/m0-web-skeleton.md |
| m0-auth | Identity Platform + authz middleware | committed | tasks/m0-auth.md |
| m0-upload | signed upload → GCS + episode create | committed | tasks/m0-upload.md |
| m0-worker-ingest | worker Job: audio + proxy + status | committed | tasks/m0-worker-ingest.md |
| m0-library | Library page, live status, playback | committed | tasks/m0-library.md |
| m0-demo-seed | make demo/dev + e2e harness + baselines | committed | tasks/m0-demo-seed.md |
| m0-ci-deploy | CI gates live + staging/prod pipeline | committed | tasks/m0-ci-deploy.md |
| m0-library-id-column | remove raw-id column from Library table | committed | tasks/m0-library-id-column.md |
| m0-env-split | staging/prod in separate GCP projects | committed | tasks/m0-env-split.md |
| m0-single-project | PoC deploy: one project, dev SA, no staging | committed | tasks/m0-single-project.md |
| m0-design-refresh | apply 2026-07-22 prototype readability refresh | committed | tasks/m0-design-refresh.md |
| m0-bun-migration | bun replaces npm (ADR 0001) | committed | tasks/m0-bun-migration.md |
| m0-dev-watch | Go auto-restart in make dev | committed | tasks/m0-dev-watch.md |
| m0-deploy-bootstrap | live-rollout fixes (5 findings, 3 commits) | committed | tasks/m0-deploy-bootstrap.md |
| m0-baselines-ci | workflow_dispatch baselines generator | committed | tasks/m0-baselines-ci.md |
| m0-ci-lint-pin | pin golangci-lint installer | committed | tasks/m0-ci-lint-pin.md |
| m0-store-org-noop | unknown org = no-op in finalizers | committed | tasks/m0-store-org-noop.md |
| m0-sveltekit-sync | explicit sync before svelte-check | committed | tasks/m0-sveltekit-sync.md |
| m0-visual-gate-tighten | 150px budget + 0.05 threshold | committed | tasks/m0-visual-gate-tighten.md |
| m0-ci-speed | parallel jobs, caching, docs skip | committed | tasks/m0-ci-speed.md |
| m0-deploy-triggers | deploy on runtime changes only, 5m watch | committed | tasks/m0-deploy-triggers.md |
| m0-gate-proofs | Deliberate-failure proofs (AC 2/3/4/6) | done (evidence in log) | tasks/m0-gate-proofs.md |
| m0-upload-fix | signBlob grant, orphan rollback, poll cadence | committed | tasks/m0-upload-fix.md |
| m0-prod-hardening-2 | CORS + least-priv worker-runner role | committed | tasks/m0-prod-hardening-2.md |
| m0-ffmpeg-pin | ffmpeg 8.1 static pin Docker+CI (ADR 0002) | committed | tasks/m0-ffmpeg-pin.md |
| m0-client-errors | FE errors → own API → Cloud Logging | committed | tasks/m0-client-errors.md |
| m0-abandoned-uploads | TTL sweep + AWAITING UPLOAD state | committed | tasks/m0-abandoned-uploads.md |
| m0-upload-protocol | server-initiated upload session (AC1 blocker) | committed | tasks/m0-upload-protocol.md |
| m0-cors-both-origins | both run.app CORS origins + PUBLIC_BASE_URL env | spec-ready | tasks/m0-cors-both-origins.md |

## M1 — the demo that sells (decomposed 2026-07-23; human pre-authorized proceeding)

Order is dependency-driven; each ≤1 day; every task through the full loop. Additive-only
migrations land inside the feature task that needs them. Per the research-first standing
rule, every task touching an external system or unfamiliar domain includes researched,
cited patterns in its spec. "Staging" in SPEC-M1's gate = the PoC prod service
(single-project mode, docs/ENVIRONMENTS.md).

| # | slug | scope kernel | status |
|---|------|--------------|--------|
| 1 | m1-lang-registry | /internal/lang registry + lang/fa normalization (ZWNJ, yeh/kaf, digits) + make eval scaffold | committed |
| 2 | m1-llm-interface | /internal/llm: schema-validated calls, retry, llm_calls audit, two engine impls, record/replay | committed |
| 2b | m1-pipeline-robustness | AC1 BLOCKER: worker 4vCPU/4h timeout, SIGTERM→failed, stale-claim sweeper | committed |
| 3 | m1-asr-interface | /internal/asr engine interface (words+timestamps, glossary biasing), neutral labels, fixtures | committed |
| 4 | m1-asr-impl | batch speech engine, chunk-stitch, gated live smoke; region us-central1 (live-verified) | committed |
| 4b | m1-pipeline-bars-fix | pipeline bars per-stage truth (human-found; design-faithful two greys) | committed |
| 4c | m1-ingest-fastpath | probe→remux fastpath (compatible masters ingest in seconds) | committed |
| 4d | m1-test-hygiene | scratch-DB isolation for DB tests; residue-tolerant asserts | committed |
| 5a | m1-stage-machine | multi-stage worker: current_stage, registry, auto-advance chaining | committed |
| 5 | m1-transcribe-stage | worker stage: audio → verbatim word-timed segments (migration 0007) | committed |
| 5b | m1-tool-pinning | pin migrate CLI via `go run -tags postgres` (go tool infeasible) | committed |
| 5c | m1-stages-config-gate | REGRESSION MITIGATION: PIPELINE_STAGES config, default ingest-only; prod-verified fixed | committed |
| 5d | m1-e2e-gates-trunk | Playwright e2e gates push-to-main rollout, fail-closed (process fix) | committed |
| 5e | m1-transcribe-reenable | transcribe ON: SIGPIPE trigger fix, fake demo, real prod engine | committed |
| 5f | m1-chirp3-switch | chirp_2 fa-IR closed by provider → chirp_3/us; drop word-confidence flag | committed |
| 6 | m1-diarize-stage | text-anchored LLM diarization, anchor-merge eval golden (parked build) | committed |
| 6b | m1-cost-safety | idempotent billable calls, per-episode attempt cap, kill switch, GCP backstops | committed |
| 6c | m1-segmentation | deterministic pause-based resegmentation (700ms gaps; 30s/60w caps) | committed |
| 6d | m1-diarize-activation | live LLM client (gemini-3.5-flash/global), 3-stage chain; S1/S2/S3 verified in prod | committed |
| 10 | m1-segments-api | GET /episodes/{id}/transcript, org-scoped neutral DTO | committed |
| 11 | m1-transcript-ui | /episode/[id] view: RTL transcript pane + proxy player | committed |
| 11b | m1-episode-delete | org-scoped soft delete + Library remove action; prod library cleaned | committed |
| 11c | m1-transcript-sync | two-way player↔transcript sync, play-state preserved (human-specified) | committed |
| 9 | m1-moments-stage | LLM ranked moments, quote-anchored word-accurate bounds (migration 0010) | committed |
| 12 | m1-moments-rail | moments API + rail; Approve/Dismiss + A/D keys (Adjust deferred to editor) | committed |
| 12b | m1-prompt-moments | free-prompt compose + approve-to-keep (migration 0011 source col) | committed |
| 12c | m1-cache-headers | shell no-cache+ETag, immutable assets, API no-store (stale-shell fix) | committed 8cc7318 (unpushed) |
| 12d | m1-pipeline-details | stage_runs provenance (two timestamps, engine labels, cost, counts) + hover details card | committed 6c3e05b (unpushed) |
| 12e | m1-diarize-scale | range-turn diarize contract (fixes 249-segment prod failure); scale fixture | in-review |
| 12f | m1-reprocess-api | POST /reprocess ready|failed→uploaded; skips bill zero; Library action | spec-ready |
| 12g | m1-youtube-ingest | YouTube URL ingestion via youtubedr (ADR 0003) | spec-ready |
| 12h | m1-llm-token-budget | thinking eats maxOutputTokens → truncation (probe-receipted); caps 32k + distinct truncation error | committed 68c0a81 |
| 7 | m1-speaker-naming | DEFERRED behind moments (not a moments prereq): evidence-gated naming, lower-third frames | queued |
| 8 | m1-shots-stage | DEFERRED behind moments (render-time concern): scdet shots + 9:16 bboxes | queued |
| 13 | m1-editor-trim | sentence-selection trim on segment/word data; J/K/L transport | queued |
| 14 | m1-editor-filmstrip | ±3s filmstrip at cut points; flash-frame warning from shots | queued |
| 15 | m1-caption-preview | live Persian caption preview (RTL, ZWNJ, token-styled) | queued |
| 16 | m1-reframe | per-shot 9:16 preview from stored bboxes; editable crop offset | queued |
| 17 | m1-fidelity-checker | caption == ASR words byte-for-byte post-normalization; blocks approval (server + UI); seeded mismatches in eval | queued |
| 18 | m1-render-stage | ffmpeg cut/crop/libass burn; .ass byte-identical goldens; fidelity-gated ready (migration: clips) | queued |
| 19 | m1-render-drawer | Reels + Telegram presets (config rows); progress; scoped signed download | queued |
| 20 | m1-corrections | segment edit rewrites words + correction_log (PG18 OLD/NEW RETURNING); glossary suggestions recorded | queued |
| 21 | m1-first-run-seed | pre-processed sample episode fixture on first login | queued |
| 22 | m1-demo-hardening | docs/DEMO.md end-to-end <15 min, zero live-processing waits | queued |

## Backlog

- m1-transcribe-stage Reviewer observations (non-blocking, 2026-07-23): (a)
  segments_episode_id_idx is redundant with the UNIQUE(episode_id, idx) prefix — drop it;
  (b) multi-chunk transcribe leaves chunk objects under proxies/ uncleaned — add cleanup
  when long-audio chunking becomes routine (harmless, org-scoped at PoC volume).
- glossary_terms table is unbuilt (bias-term plumbing is wired but passes empty); build it
  additively when a glossary task lands (moment/caption quality depends on it later).
- **m2-signals (human-proposed 2026-07-24 — ON ROADMAP, TO BE CONFIRMED before building):**
  per-segment salience indexing ("signals"; alt name "charges"): 0..many per segment,
  {kind, intensity(ordinal: none/low/high)}. Design constraints from the chat discussion:
  (1) kinds may be SPECIFIC (e.g. "far-left political view expressed") but must be
  OBJECTIVE — evidence-anchored (verbatim quote required, like moments) + written
  observable definitions; evaluative kinds (boring/brilliant) forbidden; sensitive
  classifications stay internal, never on output clips. (2) Vocabulary = config rows
  (platform-presets pattern), not code. (3) Signals FEED the holistic moment pass, never
  replace it (unit-labeling misses narrative arcs). (4) Value case = cross-episode/library
  search (deterministic filters for simple queries; compressed retrieval input for complex
  LLM queries), instant latency, timeline heat-strips/browse affordances — NOT needed at
  single-episode scale (free-prompt covers that, shipped as m1-prompt-moments). (5) Vocal
  emotion needs the future audio-LLM pass. Confirm with the human before speccing.
- m1-diagnostics-workflow (Architect-proposed 2026-07-24, after the human challenged the
  local-gcloud dependency): a workflow_dispatch GitHub Action running under the CI WIF
  identity that fetches recent worker/app logs (and optionally llm_calls triage rows) for
  a given episode and uploads them as a run artifact — Architect triggers via `gh`,
  removing the dependence on the human's 24h-expiring local gcloud session for failure
  diagnosis. Prod operation never depended on local auth (CI deploys use WIF; pipeline
  uses service accounts) — this closes the last human-in-the-loop diagnostic gap.
- llm cost undercount (reviewer-observed 2026-07-24, pre-existing): gemini
  usage.outputTokens = candidatesTokenCount only, omitting thoughtsTokenCount — audit
  cost_cents undercounts billed thinking (bounded by retry/attempt caps regardless).
  Fix = add thoughts to output count; small, additive to m1-llm-token-budget's area.
- llm_calls truncation status (implementer-raised 2026-07-24): truncated attempts audit
  as status='invalid' (sentinel + WARN log carry the distinction); optional additive
  CHECK extension to a first-class 'truncated' status for SQL triage — one-liner if wanted.
- thinkingLevel pin (implementer-raised 2026-07-24): optionally pin thinking to MEDIUM
  (today's default) to be immune to provider default drift; needs live re-verification
  post-deploy; deliberately NOT wired in m1-llm-token-budget.
- m1-deleted-gc (human-raised 2026-07-24): soft-deleted episodes keep their GCS objects —
  storage accumulates invisibly. Two-phase design: soft delete (m1-episode-delete, done
  first — reversible, audit-preserving) + a THIRD sweeper gate purging the storage prefix
  of episodes deleted_at older than DELETED_RETENTION (default 30d, env; human may pick)
  and marking the row purged (audit skeleton kept). Same idempotent/capped/logged pattern
  as the existing two sweeps. Pennies at PoC scale; build before real-user volume.
- Flaky tests in internal/pipeline/trigger_test.go (pre-existing; a small `m1-trigger-test-flake`
  task should fix BOTH — they trip the commit gate's `go test ./... -race` at low probability,
  friction that compounds as commits pile up): (1) TestExecTriggerSpawnsBinary — async
  child-process poll, 3s deadline, flaked once under load (harden poll / extend deadline);
  (2) TestCloudRunTriggerNeutralOnReject — asserts the neutral error omits HTTP status "500"
  via a bare substring grep, which false-positives when the random 16-hex error_id contains
  "500" (e.g. 50bb7b88...9500...); ~0.3%/run. Fix: token/word-boundary match, or force a
  fixed non-numeric error_id in that test. Do NOT weaken the leak assertion.
- Test hygiene: DB-backed Go tests leaked 38 rows into the shared dev DB (titles
  Orphan/Sweep/Ingest/Smoke Episode; purged operationally 2026-07-23). Tests must run in
  rolled-back transactions, a dedicated scratch database, or clean up on exit.
- Revert WATCH_MINUTES to 5–10 when real users exist.
- M2+: processing-stuck reaper, LISTEN/NOTIFY status push, updated_at trigger, remote
  staging e2e, self-hosted CI runner if GitHub minutes bite.

## Log

- 2026-07-22 — scaffold created; awaiting human review before any M0 implementation.
- 2026-07-22 — human approved scaffold + M0 execution plan (T0–T10). Claude Design export
  found in design/project/; DESIGN.md transcribed by Architect (m0-design-contract), which
  unblocks m0-web-skeleton. design/screens/*.png still pending from human — until they land,
  the prototype HTML + DESIGN.md are the Reviewer's visual ground truth.
- 2026-07-22 — m0-go-skeleton: Implementer green on first pass; Reviewer APPROVE, no findings;
  committed. m0-db-baseline spec written (spec-ready).
- 2026-07-22 — m0-db-baseline: APPROVE first pass, committed (7 deviations accepted:
  go 1.25 directive, orgs/shows public_id, NULLS NOT DISTINCT config unique, etc.). Implementer
  found make check exit-code hole → m0-check-hardening inserted, APPROVE, committed
  (proof: seeded vet+lint failures each fail the gate). Scaffold gates/CI committed
  (were untracked since initial commit).
- 2026-07-22 — m0-web-skeleton: REJECT cycle 1 (tracked build artifact under webembed/dist),
  fixed, APPROVE cycle 2, committed. Screenshots in .artifacts/screens/m0-web-skeleton/.
  Specs written for m0-auth, m0-upload, m0-worker-ingest, m0-library.
- 2026-07-22 — m0-auth: REJECT cycle 1 (IDP API key could leak into logs via url.Error on
  transport failure — Reviewer catch), fixed + regression test, APPROVE cycle 2, committed
  Accepted deviations: email as session subject (users have no public_id — closed
  prefix registry), org name-only in /me.
- 2026-07-22 — m0-upload: APPROVE first pass, committed. blob seam (gcs/localdir),
  org_ id prefix added, migration 0003 (master_size_bytes, additive). Reviewer note (non-
  blocking): local PUT lacks MaxBytesReader — dev-only seam, revisit if touched.
- 2026-07-22 — standing rules added to CLAUDE.md (generic dev identities via fixtures,
  process etiquette for agents); docs/RUNBOOK.md added with the prod first-user procedure.
- 2026-07-22 — m0-worker-ingest: implementer cut off once by API spend limit, resumed from
  transcript, completed. APPROVE first pass, committed (real-ffmpeg tests, process-
  group kill, CAS claim, neutral error_id). Accepted: no new migration needed; inline trigger
  dispatch; WORKER_TRIGGER default exec (deploy sets cloudrun). M1 backlog note: no reaper
  for episodes stuck in processing after a worker crash. Specs written for m0-demo-seed,
  m0-ci-deploy, m0-gate-proofs.
- 2026-07-22 — m0-library: APPROVE first pass, committed. Poll store (3s, visibility-
  paused), upload dialog with XHR progress, player dialog, retry CAS. Screenshot capture used
  isolated headless Chrome (own user-data-dir, only spawned PID killed) per standing rule.
  Deferred to M1/later: breadcrumb show name, live status-bar telemetry.
- 2026-07-22 — m0-demo-seed: REJECT cycle 1 (two Playwright strict-mode locator bugs that
  would have broken the CI baseline run), fixed, APPROVE cycle 2, committed. No
  Docker/Postgres in this environment: demo boot + baselines prove out in CI; visual
  baselines to be generated ONCE on the CI Linux runner post m0-ci-deploy (Architect
  authorization stands, platform-scoped filenames).
- 2026-07-22 — m0-ci-deploy: REJECT cycle 1 (Reviewer caught: runtime SA lacked run.invoker
  for the worker-Job trigger — would 403 in prod while smoke stayed green; watch probed base
  URL not candidate tag URL; error-reporting query silently zero), all fixed, APPROVE cycle 2,
  committed. Staging verification = remote smoke; full remote suite is an M1 harness
  task. ALL implementable M0 tasks done. m0-gate-proofs + baselines + prod demo blocked on
  human prerequisites (see tasks/m0-ci-deploy.md §Human prerequisites).
- 2026-07-22 — human review round: raw public ids ruled out of the UI (DESIGN.md updated;
  m0-library-id-column APPROVE first pass, committed). Environment strategy decided
  and documented in docs/ENVIRONMENTS.md: one GCP project per cloud env, local dev GCP-free;
  m0-env-split APPROVE (digest-copy promote, ENV_TIER guard), committed. Human
  prerequisites now per deploy/README.md: two projects, gcloud.sh twice, per-project
  vars/secrets.
- 2026-07-22 — human directives round 2: (a) PoC scope — ONE GCP project, no staging CD
  (m0-single-project supersedes m0-env-split's layout; ENVIRONMENTS.md to be revised);
  (b) Playwright MCP adopted (.mcp.json) + fast-UI-loop policy in CLAUDE.md (tiered checks,
  relaxed review for tiny UI diffs); (c) bun replaces npm — ADR 0001 accepted;
  (d) Go auto-restart in make dev; (e) design/ prototype refreshed by human — DESIGN.md
  updated (text-faint #8C8880, micro-type floor 10px+, Archivo-600 label rule),
  m0-design-refresh queued; (f) author identity rewritten to alitn across history;
  (g) queue de-hashed — slugs are the key, git log is the hash record. Architect manages
  the local dev server lifecycle from here on.
- 2026-07-22 — directives round 2 executed end to end: m0-single-project (rollout-on-main +
  rollback job + dev-experiments SA/bucket, APPROVE), m0-design-refresh (13-file sweep on the
  fast UI loop, APPROVE), m0-bun-migration (bun.lock, bun runtime for web checks, Playwright
  on node, zero load-bearing version drift, APPROVE), m0-dev-watch (fswatch hot-restart of
  app+worker with coherent-pair staging, APPROVE). Author identity rewritten to alitn.
  Remaining: m0-gate-proofs + baselines + prod demo on the 4-step human prerequisites in
  deploy/README.md.
- 2026-07-23 — PROD IS LIVE. Human completed prerequisites (repo, ruleset, gcloud auth);
  Architect provisioned video-clipping-503022 (infra, IAM, WIF, secrets, IdP via REST,
  demo@blueshift.local user, $50 budget). Four rollouts to green — live findings fixed
  through the loop (m0-deploy-bootstrap): PG18 needs --edition=enterprise; --no-traffic
  invalid on service creation (fail-closed bootstrap detector); jobs deploy wants
  --set-cloudsql-instances; org DRS forbids allUsers → --no-invoker-iam-check (human
  authorized by pushing); GFE intercepts /healthz on run.app → pipeline gates on /readyz.
  Rollout #4 green end to end (no-traffic → migrate → smoke → 10% → watch → 100%);
  identity-mode sign-in verified against prod; demo user mapped approver in Cloud SQL.
  m0-baselines-ci committed. Remaining: baselines commit, gate proofs, AC1 prod demo.
- 2026-07-23 — M0 ACCEPTANCE RECORD (all six criteria):
  AC1 prod upload→Ready: PENDING human gate demo (app live, sign-in verified, pipeline green).
  AC2 red PR cannot merge: PR #1 held mergeStateStatus=BLOCKED on failing required check
  through 7 runs (human accepted generic-red evidence in lieu of a dedicated red-test PR).
  AC3 drifted screenshot blocks merge: PR #1 run 29977290396 — e2e FAILED at toHaveScreenshot,
  45,070 px differing (budget 150), merge BLOCKED, diff artifact uploaded. Required TWO gate
  calibrations found by the proof itself: ratio→absolute budget, pixel threshold 0.2→0.05
  (dark-theme deltas sat under pixelmatch default — VG-1).
  AC4 red commit impossible: both hooks blocked a seeded failing test with verbatim red
  make check output (PreToolUse gate + .githooks/pre-commit exit 2); reverted clean.
  AC5 offline demo + e2e upload-to-Ready: CI runs the full Playwright flow green on the
  demo stack (baselines run + every PR e2e job).
  AC6 vendor/hex gates fire: seeded 'gemini' string and raw hex each failed make check at
  the respective gate; reverted clean.
  First full-CI hardening (all found by gates, fixed through the loop): lint-installer
  checksum pin, cross-org store ErrNoRows leak, bun-blocked svelte-kit sync, visual-gate
  calibration. CI wall-clock: ~13m (early, incl. deploys-on-every-push) → 2m54s measured
  (parallel check/e2e + caches). Deploys now fire only on runtime paths, 5m tunable watch,
  serialized. Ruleset requires check + e2e. PR #1 closed, proof branches deleted.
- 2026-07-23 — AC1 live-demo findings, all fixed through the loop: signBlob 403 (SA
  self-token-creator grant), orphan rows on failed create (rollback delete, SQL-gated),
  ~1s poll storm (non-idempotent start(); now ≤1 in-flight + one 3s timer), bucket CORS
  absent (codified with origin auto-resolve), trigger 403 runWithOverrides (custom role
  blueshiftWorkerRunner, run.developer stopgap applied then revoked). Prod upload verified
  E2E by Architect: uploaded→processing→ready→signed proxy URL. WATCH_MINUTES=0 for PoC
  (REVERT to 5-10 when real users exist). ffmpeg 8.1 pinned (ADR 0002; GPU assessed:
  cost-neutral, ~6x latency, deferred with revisit trigger). Client-error forwarding
  shipped (window errors → /api/client-errors → Cloud Logging/Error Reporting).
  M1 backlog adds: abandoned-upload TTL sweep + AWAITING UPLOAD state (human-found CORS
  orphan class — no transaction can span browser+GCS), updated_at trigger, LISTEN/NOTIFY
  for status push instead of polling.
- 2026-07-23 — AC1 attempts 2–3 (human): (a) bucket CORS listed only the legacy hash
  run.app origin while the human browsed the deterministic project-number form — both
  forms now allowed, preflights verified for each (codify: m0-cors-both-origins);
  (b) real bug behind the "CORS" 400: client sent file bytes as the body of the signed
  resumable-INITIATION POST — provider requires a bodyless init (Content-Length: 0).
  Researched provider docs + issue trackers before respeccing: adopted the documented
  server-initiated-session pattern (server does the init POST carrying the browser's
  Origin — that Origin is what makes session-URI PUTs pass browser CORS — and returns
  the session URI as a plain PUT). Client + DTO + local backend unchanged; closes the
  mock-vs-real contract gap that let this pass demo/e2e/curl smoke.
  m0-abandoned-uploads committed (a04722b): sweep gate race-safe vs concurrent
  master-key set; AWAITING UPLOAD chip; APPROVE first pass, no findings.
- 2026-07-23 — Human's first successful prod upload exposed the stuck-processing
  incident: worker Job was 1 vCPU/512Mi with 1h per-attempt timeout; a 44-min master
  was SIGKILLed mid-ffmpeg; the retry attempt no-op'd on the standing claim and exited 0
  (execution "succeeded", episode stuck, retry API rejects non-failed). Fixed through
  the loop (m1-pipeline-robustness): 4 vCPU/2Gi/4h per-attempt, SIGTERM → detached
  bounded MarkFailed (context.WithoutCancel, 5s, inside the 10s grace), additive
  episodes.claimed_at with atomic claim+stamp, stale-claim sub-sweep (PROCESSING_TTL 5h;
  NULL claimed_at = legacy stuck row → auto-unsticks the two prod episodes post-deploy).
  Human directive recorded permanently (memory + CLAUDE.md standing rule): research
  online before solving — never guess, never reinvent solved wheels.
- 2026-07-23 (later) — m1-asr-impl interrupted mid-task by the account spend limit;
  resumed by a fresh implementer that audited the inherited WIP, rewrote provenance
  comments honestly (fixtures are schema-faithful, not live captures; chunking rests on
  the documented ~20-min batch-with-timestamps cap), and finished tests + gated live
  smoke. Committed 40fdb42 after 1 review cycle (date slip — Architect-caused — and a
  missing panic-guard test). Region default switched to us-central1 after the Architect
  live-verified fa-IR word offsets there (docs table lags rollout; human challenged the
  region/feature coupling and chirp_3 rejection — re-researched, both answered: regions
  are checkpoint-rollout artifacts; chirp_3 genuinely lacks word timestamps). ASR
  foundation complete: interface + fake (1af1a22) + engine (40fdb42).
  m1-pipeline-bars-fix committed 2e857e8 (human-found: READY showed 5 identical bars;
  design defines exactly two greys — done vs border-default; Architect authorized
  regeneration of the 2 library baselines via CI post-push). Dev test-DB purged a second
  time (193 residue episodes; FK via llm_calls now breaks exact-count sweep asserts) →
  m1-test-hygiene specced (per-run scratch DB + tolerant asserts). Human directives:
  probe-first ingest fastpath (remux compatible masters in seconds; veryfast preset for
  transcodes) specced as m1-ingest-fastpath.
- 2026-07-24 (morning) — **Transcript slice shipped; real-Chirp activation ready.**
  segments-api (4feb2a8) + transcript-ui (12c3dae; 1 cycle — bg-2 canvas ruling) + episode
  baselines (21cddd0) deployed; view live in prod + :5173 (/episode/<id> via Library row).
  m1-transcribe-reenable committed (a6e8851; APPROVE): the SECOND implementer (first died
  at spend limit) found the TRUE root cause of the original regression's e2e failure — not
  env propagation (Architect's earlier hypothesis, now corrected): ExecTrigger's io.Discard
  stdio = parent-owned pipes; short-lived ingest parent exits → detached transcribe child
  takes fatal SIGPIPE pre-claim (proven rc=-13). Fix: nil Stdout/Stderr/Env (null-device
  stdio, inherited env), pinned by test. Prod worker env verified key-exact vs config
  (ASR_ENGINE_MODE=speech, chirp_2, fa=fa-IR, PIPELINE_STAGES=ingest,transcribe, cloudrun
  self-trigger). Cost-safety proven in-chain (1 attempt; re-drive bills zero).
  OPERATIONAL: Speech service-agent bucket grant was documented in RUNBOOK but NOT live —
  Architect granted + verified roles/storage.objectViewer for the speech service agent
  (verify-don't-assume vindicated). Human budget alert set 2026-07-24.
  **chirp_3 re-verified on human challenge — DOCS OVERTURNED BY LIVE TEST (receipts):**
  the feature table says word timestamps "not supported" for chirp_3, but a live
  us-multiregion sync recognize (fa-IR, enableWordTimeOffsets, same 30s broadcast sample)
  returned 84 words WITH offsets — the docs are wrong/stale; the human's challenge was
  right. HOWEVER: fa on chirp_3 is Preview (no SLA); the diarization language list still
  EXCLUDES Persian (so no free diarization either way); and the first-token quality
  wobbled on the very same audio (chirp_2: "سیزدهم مهرماه" ✓ vs chirp_3: "۱۶همم مهر ماه" —
  morphologically broken + different compound tokenization, which matters for caption
  fidelity). DECISION: chirp_2 stays the default on quality/stability grounds TODAY; the
  batch 20-min-with-timestamps cap is already handled by our engine-agnostic chunk+stitch
  (PlanChunks/StitchTranscripts) so no workaround is needed for either engine. BACKLOG:
  m1-asr-engine-eval — data-driven chirp_2 vs chirp_3 bake-off (WER on longer fa fixtures,
  tokenization/ZWNJ behavior, cost/latency) once fa-IR exits Preview or earlier if quality
  becomes a complaint; switching is config + one impl task behind /internal/asr. BACKLOG: no reprocess path exists for old READY episodes (claim gate
  correctly refuses ready; RUNBOOK SQL reset needs DB access) — small m1-reprocess-api
  task later; fresh uploads flow through the full chain automatically.
- 2026-07-23 (night) — **REGRESSION found + owned.** The transcribe stage (c641226) was
  wired into the worker auto-advance chain AND deployed, but was not deployment-ready:
  (1) the prod worker Job has NO ASR env, so with ENV=prod (ASRMode default `speech`) the
  transcribe engine can't build → a NEW prod upload gets stuck in processing/transcribe
  (existing READY episodes unaffected); (2) the demo/CI upload→READY auto-advance path
  doesn't run the transcribe worker in fake mode → main's Playwright e2e went RED
  (flow.spec upload→READY timeout + token-conformance stale single-stage bar expectation).
  Surfaced by a FAILED baselines.yml run (the artifact's PNGs were byte-identical to
  committed, which was the tell — a failed regen, not a clean drift; NOT committed).
  **Root process gap:** `make check` (commit gate) excludes Playwright e2e, and the loop
  pushes directly to main, so pr.yml's e2e job never ran on the commits — AC5/visual gates
  have not actually guarded the trunk since the M0 proof PR. Architect miss: authorized the
  transcribe push on the implementer's "flow.spec still holds" claim without an e2e gate.
  Response: m1-stages-config-gate (mitigation, PIPELINE_STAGES default ingest-only —
  restores prod + trunk green, transcribe code parked not deleted; MANDATORY make e2e in
  its acceptance); m1-e2e-gates-trunk (make Playwright e2e gate push-to-main, fail-closed);
  m1-transcribe-reenable (demo auto-advance fake-env fix + two-stage e2e + prod ASR, the
  last GATED on a human decision about paid Cloud Speech in prod). tool-pinning also
  revised: `go tool` directive is infeasible for golang-migrate v4 (drivers build-tag
  gated; go.sum 180→866) → `go run -tags postgres` (zero new deps, version-locked).
- 2026-07-23 (evening) — Fastpath wave deployed green (rollout of 40fdb42+2e857e8+8127b0b
  +81bbc1d): compatible masters now remux in seconds (the human's 170MB/44-min file
  class); pipeline bars honest per design. Visual baselines: 2 library PNGs regenerated
  via the baselines workflow under explicit Architect authorization and committed by the
  Architect (login PNGs verified byte-identical). m1-test-hygiene committed (d343e53;
  APPROVE first pass; per-run scratch DBs, dirty-server proof) — DB tests can no longer
  litter or read the named database; final residue purge performed (34 rows, the last
  one needed). Next: m1-stage-machine → m1-transcribe-stage (specs written).
  (no word timestamps at all; fa preview-only). chirp_2 fa-IR is served only from
  asia-southeast1; Architect ran a live sync recognize on real Persian broadcast audio:
  full transcript + 82 words with sane monotonic start/end offsets. m1-asr-impl specced
  with the remaining unknown (possible 20-min batch cap with word timestamps) as a
  mandatory in-task experiment with a chunk-and-stitch fallback design.
- 2026-07-23 — M1 wave 1: m1-lang-registry committed (ccb00dd; 1 review cycle — citation
  + fixture-note findings fixed; UCD data embedded and independently verified).
  m1-llm-interface committed (2748508; APPROVE first pass; Claude structured outputs
  verified GA as output_config.format, no beta header; llm_calls gains additive status
  column, migration 0004). Dev DB purged of 38 test-residue rows (operational).
- 2026-07-23 — **M0 GATE CLOSED — AC1 accepted.** The human's own browser upload of a
  44-minute Persian master (ep_06frvp5anxrgbahax…) reached READY on prod with a playable
  signed proxy (verified: proxy endpoint 200, GCS range request 206). Full recovery loop
  proven live in sequence: stuck→failed via the new stale-claim sweeper on its first
  post-deploy pass, retry via the existing API (200), re-ingest on the resized worker
  (4 vCPU: ~20 min for the 44-min master vs >60-min timeout death before). All six M0
  acceptance criteria now have recorded evidence (AC2–AC6 in the earlier acceptance
  record). Rollout 29996850621 also applied migrations 0004+0005. M1 execution continues
  per the decomposition above (human pre-authorized proceeding in absence).
- 2026-07-24 (late — NEWEST ENTRY; tail order above is historical) — **Moments arc closed;
  scale fix in.** Evening waves, all APPROVE: moments-stage (149c83a) + moments-rail
  (669a66d) + prompt-moments (d1ac797, live compose verified on the 3-speaker clip: real
  composed moment from a free prompt, verbatim quote, word-accurate bounds) +
  cache-headers (8cc7318, normal-refresh fix) + pipeline-details (6c3e05b, stage_runs
  provenance: two timestamps per stage, engine label public/detail private, cost, item
  counts, hover card; bs-asr-2 label bump). **Full-episode receipt:** the human's 44-min
  sample0 processed clean through transcribe (249 segments / 6,463 words) then FAILED at
  diarize — flat per-idx contract brittle at scale → m1-diarize-scale (04a97c3, APPROVE,
  zero findings): range-turn contract {turns:[start_idx,end_idx,speaker_key]} with exact
  0..n-1 tiling validation, 249-segment synthetic eval fixture, prompt v2, cost-safety
  diff empty. Architect: RUNBOOK engine-label registry added; ADR 0003 accepted
  (youtube ingestion via youtubedr; YT transcripts declined — no word timestamps); specs
  written for m1-reprocess-api + m1-youtube-ingest. PENDING at entry time: batch push
  (8cc7318+6c3e05b+04a97c3+architect) → one deploy → RETRY the failed full episode
  (transcribe skips free) → moments on the 44-min. Human action pending: gcloud auth
  login (24h org expiry; Architect log reads blocked, deploys unaffected).
- 2026-07-24 (later — NEWEST) — **Diarize failure #2 root-caused by live probes; fixed.**
  Post-deploy retry of the 44-min episode failed again at diarize (3 stage attempts,
  6/6 engine calls "failed validation", 12¢, attempts now 7/10). Provenance API +
  Cloud Logging narrowed it; three wire-identical Architect probes settled it:
  finishReason MAX_TOKENS on 2/3 — THINKING tokens (5.6–7.9k at 249 segments) count
  against maxOutputTokens=8192, truncating the answer JSON; the range contract itself
  is fine (probe 1: valid 27-turn tiling, S1–S3, 1,103 answer tokens). Fix
  m1-llm-token-budget (68c0a81, APPROVE first pass): caps 32k in diarize/moments/
  compose, distinct neutral truncation error keyed on the provider finish signal
  (short-circuits before decode), thinkingLevel researched + documented (not wired —
  Gemini-3 coarse levels only; default MEDIUM is what probes ran), Temperature:0
  omitempty lie removed. Reviewer observation backlogged: audit cost omits
  thoughtsTokenCount (undercount, pre-existing). Next: push → deploy → FINAL retry
  (one full stage cycle left before the attempt cap).
