# ADR 0003 — YouTube as an upload source via the youtubedr library

**Status:** Accepted (human-directed 2026-07-24) · **Scope:** ingestion

## Decision

Add YouTube-URL ingestion beside local-file upload. Server-side download via the Go
library `github.com/kkdai/youtube/v2` (CLI name `youtubedr`) — a maintained, pure-Go
downloader. This is a NEW DEPENDENCY, approved by the human on 2026-07-24 (Occam rule
satisfied).

## Rationale & constraints

- Pure Go (no yt-dlp/python sidecar), library-first (we call it in-process from the
  worker, not as a CLI), active upstream.
- **Fragility is inherent:** YouTube changes frequently and breaks downloaders. Handling:
  (1) the dependency is version-pinned like everything else; (2) a nightly live smoke
  (`-tags live`, skipped in CI) downloads metadata for a stable public video and opens an
  issue on failure — the drift detector; (3) RUNBOOK documents the bump procedure
  (update dep → run live smoke → normal loop). Expect periodic bumps.
- **Quality/format:** request 1080p progressive/muxed MP4 (H.264/AAC) — this passes the
  ingest remux fastpath, so the downloaded file serves as BOTH master and near-instant
  proxy source. Fallback to the best available ≤1080p muxed format.
- **YouTube transcripts: deliberately NOT ingested.** The library can fetch caption
  tracks, but they carry segment-level timing only — no word timestamps — and word
  timing is load-bearing across the pipeline (caption burn, editor trim, fidelity
  checking, quote-anchored moment bounds). A transcript that cannot be word-anchored
  cannot feed anything downstream; ASR costs ~$1/hour. Revisit only if a word-timed
  source appears.
- **Rights/ToS:** the tool ingests content the org has rights to process (their own
  channel/footage). Responsibility for rights sits with the operator, as with local
  uploads. Not a distribution feature.
- Provenance: `episodes.source` ('upload' | 'youtube') + the source URL recorded
  privately; ingest's stage_runs metadata records the fetch (per the stage-provenance
  design).

## Consequences

Upload dialog gains a URL tab; a server-side fetch path runs inside the ingest stage
(download → then the existing probe/remux/transcode flow); one new pinned Go dependency
with a nightly canary.
