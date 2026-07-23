# Task: m1-ingest-fastpath — probe first; remux when possible; transcode fast when not

**Milestone:** M1 (human-directed efficiency) · **Type:** backend (media) · **Slug:** `m1-ingest-fastpath`

## Problem (human report, 2026-07-23)

A 170 MB, 44-min, already-H.264/AAC mp4 spent ~20 min in proxy transcode on the 4-vCPU
worker. Re-encoding an already-browser-compatible master is wasted work; and when we do
transcode, the current preset is slower than a disposable proxy warrants.

## Research requirement (standing rule)

Ground the eligibility rules and ffmpeg flags in documented behavior (MDN/browser H.264
support levels, ffmpeg remux/faststart docs, x264 preset guidance). Cite in code
comments. Do not invent codec-compatibility rules.

## Scope

1. **Probe stage (ffprobe, /internal/media):** structured probe of the master — container,
   video codec/profile/level, dimensions, bitrate, audio codec, duration. Persist probe
   summary in the worker log (server-side only).
2. **Eligibility ruling (documented, config-tunable):** master goes REMUX path when ALL:
   H.264 (profile ≤ High, level ≤ 4.2), AAC audio, mp4/mov container, height ≤ 1080,
   overall bitrate ≤ PROXY_MAX_REMUX_BITRATE (env, default ~6 Mbps). Else TRANSCODE.
3. **Remux path:** `ffmpeg -c copy -movflags +faststart` to the proxies/ object —
   seconds, not minutes. Proxy object contract unchanged (player always plays proxies/).
4. **Transcode path speed:** proxy encodes switch to `-preset veryfast` (document the
   quality rationale for disposable proxies; keep CRF per current setting), explicit
   `-threads 0`. Expected ≥2× speedup at 4 vCPU.
5. **Audio extraction unchanged** (ASR still needs it in both paths).
6. **Tests:** /internal/media probe parsing (fixture probe outputs); eligibility table
   tests incl. boundary cases; remux + transcode integration tests against the existing
   small test media fixtures (real ffmpeg, as media tests already do); worker stage test
   covering both paths. Golden behavior: remuxed proxy must still pass the existing
   playability assertions used for transcoded proxies.

## Out of scope

GPU (ADR 0002 trigger unmet); changing worker CPU; per-shot/clip rendering.

## Acceptance

- make check green. Reviewer verifies: eligibility rules cited; remux output faststart'd
  and playable per existing assertions; no behavior change for ineligible masters other
  than preset; no vendor terms outside allowed zones.
- Post-deploy (Architect, operational): re-upload-class check — a compatible master
  ingests in well under a minute of worker time.

## Evidence

Summary; diffs; test transcript; cited sources; open questions.
