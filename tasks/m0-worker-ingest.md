# Task: m0-worker-ingest — worker Job, ffmpeg ingest stage, status machine

**Milestone:** M0 (docs/SPEC-M0.md §7) · **Type:** backend/pipeline · **Slug:** `m0-worker-ingest`

## Goal

`cmd/worker <episode_public_id> <stage>` runs stage `ingest`: extract audio + render a
browser-playable proxy into `proxies/`, driving episode status
`uploaded → processing → ready | failed` with per-stage timeout and retries=2. Upload-complete
triggers the worker automatically in both dev (subprocess) and cloud (Cloud Run Jobs) modes.

## Architect rulings

- **ffmpeg via `os/exec`** in `internal/media` — no Go media dependencies. `ffprobe` for
  duration. Timestamps/durations come only from ffmpeg/ffprobe (verbatim invariant).
- **Outputs:** proxy `{org}/{ep}/proxies/proxy-720p.mp4` (H.264 high, ≤720p height preserved
  aspect, AAC, `+faststart`) and audio `{org}/{ep}/proxies/audio.flac` (mono 16 kHz — ASR
  input for M1). Keys via the same builder as m0-upload (public ids only).
- **Trigger seam** (`internal/pipeline`): `Trigger` interface with `exec` impl (spawns the
  worker binary — dev/demo) and `cloudrun` impl (Cloud Run Jobs REST `run.googleapis.com`
  executions endpoint, auth via metadata-server token, stdlib only; provider hostname stays
  in this internal package and server logs). Selected by `WORKER_TRIGGER=exec|cloudrun`
  (+`WORKER_BIN` for exec, job name/region envs for cloudrun). `POST /api/episodes/{id}/upload-complete`
  (from m0-upload) now fires the trigger after verifying the object.
- **Status machine is the worker's job:** worker claims the episode
  (`uploaded → processing`, org-scoped compare-and-set — a second concurrent invocation must
  no-op), runs the stage under a per-stage timeout (`INGEST_TIMEOUT` default 30m), retries
  the stage up to 2 more times on failure (fresh tmpdir each attempt), then `ready`
  (+ `proxy_object_key`, `duration_ms`) or `failed` (+ neutral `error_id`; raw ffmpeg stderr
  tail to server logs only).

## Scope

1. **`internal/media`:** `ProbeDuration(ctx, path)`, `RenderProxy(ctx, in, out)` ,
   `ExtractAudio(ctx, in, out)` — thin, arg-explicit ffmpeg wrappers; context cancellation
   kills the process group; stderr captured (log-only).
2. **`internal/pipeline`:** stage registry (`ingest` only), claim/retry/timeout/finalize logic
   over `internal/store` + `internal/blob` (GCS mode: download master to tmpdir, upload
   outputs; local mode: direct paths). Compare-and-set sqlc queries added (additive).
3. **`cmd/worker`:** arg parsing (`<episode_public_id> <stage>`), config load, structured
   logs (reuse `internal/logx`), exit 0/1 for Cloud Run Jobs semantics.
4. **Trigger** per ruling + wiring into upload-complete.
5. **Tests:** pipeline with a fake media runner + local blob: happy path (status/keys/duration
   recorded), retry-then-success, retries-exhausted → failed + error_id neutrality, timeout
   kills attempt, concurrent-claim no-op, cross-org isolation. Media wrappers: if `ffmpeg`
   is on PATH, generate a 2s test clip (`ffmpeg -f lavfi testsrc`) in a temp dir and assert
   real proxy/audio/probe outputs (dimensions, faststart atom, duration ±50ms); skip cleanly
   with a logged reason when ffmpeg is absent. Trigger: exec impl spawns a stub binary;
   cloudrun impl against a fake local HTTP server (asserts path/auth header shape, neutral
   error mapping). All race-clean.

## Out of scope

Any other stage (transcribe/analyze are M1), SSE/status UI (m0-library), CI ffmpeg
installation (m0-ci-deploy), scdet/cut/crop/captions (M1), Eventarc/queues (forbidden without
ADR anyway).

## Acceptance

- `make check` fully green (with and without ffmpeg present — skips are logged, not silent).
- Local end-to-end transcript (requires ffmpeg locally): dev login → create episode → PUT the
  generated 2s fixture → upload-complete (trigger=exec) → worker runs → row status `ready`,
  `proxy_object_key` set, `duration_ms` ≈ 2000; proxy file exists under
  `BLOB_DIR/{org}/{ep}/proxies/` and plays (probe reports h264+aac, faststart).
- Failure path transcript: corrupt/zero-byte master → after 3 attempts status `failed`,
  `error_id` set, response/API surfaces neutral text only.

## Evidence to return

Summary + deviations; diffstat + status; tail of `make check`; both transcripts; open questions.
