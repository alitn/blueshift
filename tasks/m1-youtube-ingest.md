# Task: m1-youtube-ingest — ingest an episode from a YouTube URL (ADR 0003)

**Milestone:** M1 · **Type:** full-stack · **Slug:** `m1-youtube-ingest`
**Authority:** docs/adr/0003-youtube-ingestion.md (Accepted). New dependency
`github.com/kkdai/youtube/v2` is human-approved; pin the version in go.mod like all deps.

## Shape

A second way to create an episode: paste a YouTube URL instead of choosing a file. The
server downloads the video into `masters/` and the episode then flows through the
EXISTING pipeline unchanged (ingest probe→remux fastpath expected for 1080p muxed MP4;
then transcribe/diarize/moments as configured). No new stage; the download happens
inside the ingest stage before probe.

## Scope

1. **Migration (additive):** `episodes.source text NOT NULL DEFAULT 'upload'` +
   CHECK `source IN ('upload','youtube')`; `episodes.source_url text NULL` (private —
   never in any DTO; internal provenance only, like engine_detail).
2. **API:** `POST /api/episodes/from-url` — auth + org-scoped; body `{url, title?}`.
   Validate the URL is a YouTube video URL server-side (host allowlist:
   youtube.com/youtu.be forms; reject playlists/channels/live). Creates the episode row
   (`source='youtube'`, status `uploaded`, title from request or video metadata) and
   triggers ingest (same best-effort trigger as upload-complete). Rate-limit like
   compose (per-org, small burst) — the fetch is expensive.
   NEUTRALITY: "YouTube" as a SOURCE name is user-facing product vocabulary and allowed
   in UI strings and the `source` enum (it names the user's own source, not our stack).
   Library/provider errors still map to neutral messages + internal error IDs at the
   boundary; raw errors to server logs only.
3. **Worker (ingest stage):** when `source='youtube'` and no master object exists:
   download via the library IN-PROCESS — prefer 1080p progressive/muxed MP4 (H.264/AAC),
   fallback best-available ≤1080p muxed — stream to GCS `{org}/{episode}/masters/…`,
   then continue into the existing probe/remux/transcode flow untouched.
   - **Idempotent per unit of work:** master object exists ⇒ skip download (re-drives
     never re-fetch). Bounded: single download attempt per stage attempt; the stage's
     existing retry/attempt-cap machinery is the only loop. Download failure ⇒ stage
     `failed` with a neutral message; RETRY works.
   - Enforce a size/duration ceiling (config; default 4h/8GB) — reject beyond it
     pre-download from metadata.
4. **UI:** upload dialog gains a second tab/mode: "From URL" (design tokens only;
   neutral copy — field label "Video URL"). Paste → validate → create → the Library row
   appears in the normal processing flow. Keyboard path + errors surfaced inline.
5. **Tests:** engine behind a tiny internal interface so tests use a fake (recorded
   metadata + a small fixture file streamed as the "download"); NO live YouTube in
   check/demo/CI. DB-backed tests for from-url transition + org scoping + URL
   validation (including rejection forms). E2E: URL-tab flow against make demo with the
   fake fetcher. Nightly live smoke (`-tags live`, skipped in CI): metadata-only fetch
   of one stable public video; failure opens an issue (drift canary per ADR).
6. **RUNBOOK:** dependency-bump procedure (update pin → live smoke → normal loop).

## Out of scope

- YouTube caption/transcript ingestion (declined in ADR 0003 — no word timestamps).
- Playlists, channels, live streams, member/age-gated content (reject cleanly).
- Any scheduler/watcher (one URL = one episode, human-initiated).

## Acceptance

- make check green; e2e URL flow green offline; zero baseline drift beyond the upload
  dialog (visual baseline update for the dialog is expected — Architect authorizes it
  for the changed dialog only).
- Reviewer verifies: vendor-neutrality boundaries (source naming allowed; library
  errors mapped), cost/idempotency invariants (no unbounded fetch loops, skip on
  existing master), URL validation strictness, DTO privacy of source_url.
- Architect post-deploy: ingests one real public YouTube video in prod end-to-end.

## Evidence

Summary; diffstat; fake-fetcher chain transcript; e2e + screenshots of the URL tab;
open questions.
