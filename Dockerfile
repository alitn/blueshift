# ==============================================================================
# Blueshift Studio — one image, both binaries (app + worker).
#
#   stage 1 (web)     oven/bun  → bun install && bun run build → web/build
#   stage 2 (build)   golang    → copy the SPA into internal/webembed/dist,
#                                  go build ./cmd/app ./cmd/worker (static, CGO off)
#   stage 3 (ffmpeg)  debian    → download + sha256-verify the pinned BtbN
#                                  ffmpeg 8.1.x static GPL build (ADR 0002)
#   stage 4 (runtime) debian    → ca-certificates + pinned ffmpeg/ffprobe,
#                                  non-root, both binaries
#
# ENTRYPOINT is the API server (/app/app). The pipeline worker runs from the
# SAME image on a Cloud Run Job via `--command /app/worker` + args
# (see .github/workflows/deploy.yml and deploy/gcloud.sh). Migrations do NOT
# ship as a third binary: they run from CI against Cloud SQL through the auth
# proxy using the checked-out migrations/ tree — the single migration source
# shared with `make demo` and the DB-backed tests.
# ==============================================================================

# ---- stage 1: web build ------------------------------------------------------
# bun is the web package manager + build runtime (ADR 0001). oven/bun ships bun
# without Node, so the build forces the bun runtime (`bun --bun run build`); the
# native rollup/esbuild optional deps then match bun's arch. Pinned to the local
# bun version for reproducibility.
FROM oven/bun:1.3.14-slim AS web
WORKDIR /src/web
# Install deps against the lockfile first so the layer caches across source edits.
# --frozen-lockfile fails the build if bun.lock and package.json drift.
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
# adapter-static writes the SPA to web/build (fallback index.html for the SPA
# router). tokens.css is committed under src/lib, so the build is self-contained.
RUN bun --bun run build

# ---- stage 2: go build -------------------------------------------------------
FROM golang:1.25-bookworm AS build
WORKDIR /src
# Module graph first for layer caching.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Overlay the freshly built SPA onto the embed dir (the checked-in tree carries
# only .gitkeep). //go:embed all:dist then picks up the real build.
COPY --from=web /src/web/build/ ./internal/webembed/dist/
# Static, pure-Go binaries (pgx has no cgo) so debian-slim needs no libc extras.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/app ./cmd/app \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/worker ./cmd/worker

# ---- stage 3: ffmpeg (pinned, checksum-gated) --------------------------------
# ffmpeg 8.1.x pinned to a BtbN FFmpeg-Builds static GPL release (ADR 0002) for
# dev/prod/CI parity — debian's apt ffmpeg is 5.x/6.x, a major-version skew from
# dev's 8.1.2. Pinned to an IMMUTABLE dated autobuild tag (never the rolling
# `latest`) and sha256-verified at build: a wrong checksum fails `sha256sum -c`
# and the build stops, so the download can never drift. The linux64 GPL
# (non-shared) build is a self-contained ffmpeg+ffprobe (all codec libs
# statically linked; only glibc + libgcc_s dynamic) with libx264, native aac,
# native flac, +faststart, and libass (+freetype/fribidi/harfbuzz for RTL/fa
# caption shaping). LOCKSTEP with .github/workflows/{pr,baselines}.yml — bump
# tag + asset + sha256 in all four places together.
#   tag:    autobuild-2026-07-22-13-36
#   asset:  ffmpeg-n8.1.2-30-g45f1910444-linux64-gpl-8.1.tar.xz
#   sha256: 4ad0d6eb98bde796841050cf12bf9428e188446bd518b245fb4aa02f25b633a0
FROM debian:bookworm-slim AS ffmpeg
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl xz-utils \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /ff
RUN set -eux; \
    tag=autobuild-2026-07-22-13-36; \
    asset=ffmpeg-n8.1.2-30-g45f1910444-linux64-gpl-8.1.tar.xz; \
    sha=4ad0d6eb98bde796841050cf12bf9428e188446bd518b245fb4aa02f25b633a0; \
    curl -sSfL -o ffmpeg.tar.xz \
      "https://github.com/BtbN/FFmpeg-Builds/releases/download/$tag/$asset"; \
    echo "$sha  ffmpeg.tar.xz" | sha256sum -c -; \
    tar -xf ffmpeg.tar.xz --strip-components=1; \
    rm ffmpeg.tar.xz
# Version is not asserted here (the amd64 binary need not run on the build host,
# which may be arm64); the checksum pins the exact bytes, and CI runs the
# `ffmpeg -version` 8.1 assert on an amd64 runner.

# ---- stage 4: runtime --------------------------------------------------------
FROM debian:bookworm-slim AS runtime
# ca-certificates for outbound TLS (GCS, IdP); libgcc-s1 provides libgcc_s.so.1,
# the one non-glibc shared lib the pinned static ffmpeg links. NO apt ffmpeg —
# it comes from the pinned `ffmpeg` stage above (ADR 0002).
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates libgcc-s1 \
 && rm -rf /var/lib/apt/lists/*
# Pinned ffmpeg + ffprobe only (ffplay is not shipped). See the `ffmpeg` stage.
COPY --from=ffmpeg /ff/bin/ffmpeg  /usr/local/bin/ffmpeg
COPY --from=ffmpeg /ff/bin/ffprobe /usr/local/bin/ffprobe
# Non-root, no login shell, stable uid for Cloud Run.
RUN useradd --system --uid 10001 --home-dir /app --shell /usr/sbin/nologin blueshift
WORKDIR /app
COPY --from=build /out/app /app/app
COPY --from=build /out/worker /app/worker
USER 10001:10001
EXPOSE 8080
# API server by default; the worker Job overrides with `--command /app/worker`.
ENTRYPOINT ["/app/app"]
