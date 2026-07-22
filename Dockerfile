# ==============================================================================
# Blueshift Studio — one image, both binaries (app + worker).
#
#   stage 1 (web)     node:22   → npm ci && npm run build  → web/build
#   stage 2 (build)   golang    → copy the SPA into internal/webembed/dist,
#                                  go build ./cmd/app ./cmd/worker (static, CGO off)
#   stage 3 (runtime) debian    → ffmpeg only, non-root, both binaries
#
# ENTRYPOINT is the API server (/app/app). The pipeline worker runs from the
# SAME image on a Cloud Run Job via `--command /app/worker` + args
# (see .github/workflows/deploy.yml and deploy/gcloud.sh). Migrations do NOT
# ship as a third binary: they run from CI against Cloud SQL through the auth
# proxy using the checked-out migrations/ tree — the single migration source
# shared with `make demo` and the DB-backed tests.
# ==============================================================================

# ---- stage 1: web build ------------------------------------------------------
FROM node:22-bookworm-slim AS web
WORKDIR /src/web
# Install deps against the lockfile first so the layer caches across source edits.
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
# adapter-static writes the SPA to web/build (fallback index.html for the SPA
# router). tokens.css is committed under src/lib, so the build is self-contained.
RUN npm run build

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

# ---- stage 3: runtime --------------------------------------------------------
FROM debian:bookworm-slim AS runtime
# ffmpeg for the media pipeline; ca-certificates for outbound TLS (GCS, IdP).
RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg ca-certificates \
 && rm -rf /var/lib/apt/lists/*
# Non-root, no login shell, stable uid for Cloud Run.
RUN useradd --system --uid 10001 --home-dir /app --shell /usr/sbin/nologin blueshift
WORKDIR /app
COPY --from=build /out/app /app/app
COPY --from=build /out/worker /app/worker
USER 10001:10001
EXPOSE 8080
# API server by default; the worker Job overrides with `--command /app/worker`.
ENTRYPOINT ["/app/app"]
