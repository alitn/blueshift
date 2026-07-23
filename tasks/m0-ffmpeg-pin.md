# Task: m0-ffmpeg-pin — ffmpeg 8.1.x pinned in Docker + CI (ADR 0002)

**Milestone:** M0 tooling · **Type:** build/CI · **Slug:** `m0-ffmpeg-pin`

## Scope

1. **Dockerfile stage 3:** replace `apt install ffmpeg` with a pinned BtbN FFmpeg-Builds
   static release (linux64 GPL, exact release tag pinned, sha256 verified at build; fail
   the build on mismatch). Install ffmpeg + ffprobe to /usr/local/bin. Keep ca-certificates.
   Choose the latest 8.1.x asset available; record tag + sha in the Dockerfile comment.
2. **CI (pr.yml both jobs where ffmpeg installs today, baselines.yml):** same pinned
   static build via a small shared step (curl + sha256sum -c), replacing apt ffmpeg.
   Version-assert after install: `ffmpeg -version | head -1` must contain " 8.1".
3. **make setup:** warn (not fail) if local ffmpeg major.minor < 8.1.
4. **Sanity:** the media tests exercise real ffmpeg — run `go test ./internal/media -race`
   locally against local 8.1.2 (present) and confirm CI asset choice supports libx264,
   aac, flac, faststart (BtbN GPL builds do — verify the flags list from the release notes
   or by downloading and probing the linux binary is NOT possible on darwin; verify via
   the build's published config instead and state how you verified).

## Acceptance

- make check green; YAMLs parse; Docker build proof deferred to CI as before (no local
  daemon) with the checksum making drift impossible.
- Reviewer verifies: checksum actually gates (a wrong sha fails), version assert present
  in CI, no apt ffmpeg remains anywhere, ADR 0002 referenced in comments.

## Evidence

Summary; diffs; the pinned tag + sha; how codec support was verified.
