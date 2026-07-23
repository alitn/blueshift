# ADR 0002 — ffmpeg 8.1 pinned everywhere; GPU transcoding assessed, deferred

**Status:** Accepted (human-directed 2026-07-23) · **Scope:** media toolchain

## Decision 1 — pin ffmpeg 8.1.x in every environment

Local dev already runs 8.1.2. Prod (Docker) and CI currently get whatever distro apt ships
(Debian bookworm ≈ 5.x/6.x — a major-version skew against dev). Pin a single 8.1.x static
Linux build (BtbN FFmpeg-Builds release, exact tag + sha256 verified at download) in the
Docker runtime stage and both CI workflows; `make setup` warns if local major < 8.

Honest note on benefit: 8.x headline features (Vulkan compute codecs FFv1/ProRes, AV1
Vulkan encoder, D3D12, JPEG-XS, Whisper filter) do not change our current H.264/AAC/libass
pipeline. The pin's real value is dev/prod parity, current bug/security fixes, and having
8.x capabilities (AV1 encode, Vulkan) available when M1+ wants them.

## Decision 2 — GPU (deferred; revisit trigger below)

Assessment for our workload (researched 2026-07-23):

- **Workload:** per-episode ingest = 1–2 h master → 720p H.264 proxy + audio; M1 adds
  short clip cuts + caption burn (libass = CPU-side filter regardless of encoder).
- **Speed:** NVENC ≈ 5–15× realtime vs libx264 ≈ 0.5–2× — a 2 h master drops from
  ~60–120 min (2 vCPU) to ~10–20 min on an L4.
- **Quality:** at proxy/social bitrates NVENC ≈ x264 medium; differences negligible for
  our outputs (Turing+ NVENC ≥ x264 medium).
- **Platform:** Cloud Run jobs support NVIDIA L4 in us-central1 (our region); GPU
  ≈ $0.67/h (no zonal redundancy) billed per second, plus the GPU tier's 4 vCPU/16 GiB
  floor; drivers injected by the platform; needs `--gpu 1 --gpu-type nvidia-l4` + quota.
- **Cost per 2 h episode (rough):** CPU ≈ $0.29 (90 min × 2 vCPU) vs GPU ≈ $0.30
  (15 min × L4+4vCPU/16GiB). **Cost-neutral; purely a latency play (~6×).**

**Deferred because:** PoC volume is tiny, the "≈25 min processing" product promise is met
by CPU for typical episodes, and GPU adds quota + driver + nvenc-build surface now.
**Revisit when:** episodes regularly exceed ~1 h masters AND ingest latency matters to
users, or M1 render queues back up. The change is contained: Job flags + an nvenc-enabled
ffmpeg build + `h264_nvenc` arg switch in `/internal/media`.

Sources: Cloud Run GPU jobs docs & pricing (docs.cloud.google.com/run — GPU for jobs;
~$0.0001867/s L4 Tier-1), FFmpeg 8.0/8.1 release notes (ffmpeg.org, Phoronix), NVENC vs
x264 benchmarks (NVIDIA Turing blog, chipsandcheese, streaminglearningcenter).
