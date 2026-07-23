// Package media wraps ffmpeg and ffprobe as plain external processes (os/exec,
// no cgo, no Go media libraries). It is the only place the pipeline turns a
// source master into a browser proxy and an ASR audio track, and the only place
// a media duration is measured — the verbatim invariant means durations and
// timestamps come from ffprobe/ffmpeg, never from anything that guesses.
//
// Every wrapper is arg-explicit (no shell, no string interpolation of paths),
// runs its child in its own process group so a cancelled context tears down the
// whole ffmpeg subtree, and captures a bounded tail of stderr. That stderr is
// for server logs only: it can name codecs and the underlying tool, so the
// callers (internal/pipeline) map any failure to a neutral, id-tagged client
// error and keep the raw text server-side.
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Binaries invoked by the wrappers. Resolved from PATH; Available() reports
// whether they are present so tests can skip cleanly rather than fail.
const (
	ffmpegBin  = "ffmpeg"
	ffprobeBin = "ffprobe"
)

// stderrTailBytes bounds how much of a child's stderr is retained for logging.
// ffmpeg is verbose; the tail holds the actual error without unbounded memory.
const stderrTailBytes = 4 << 10

// ErrUnavailable reports that the required media binary is not on PATH. Callers
// treat it as an environment fault, distinct from a media-processing failure.
var ErrUnavailable = errors.New("media: ffmpeg/ffprobe not found on PATH")

// Available reports whether both ffmpeg and ffprobe are resolvable on PATH.
// Tests use it to skip real-media assertions with a logged reason.
func Available() bool {
	if _, err := exec.LookPath(ffmpegBin); err != nil {
		return false
	}
	if _, err := exec.LookPath(ffprobeBin); err != nil {
		return false
	}
	return true
}

// Runner adapts the package-level wrappers to a value the pipeline injects as
// its Media seam. It is stateless; the zero value is ready to use.
type Runner struct{}

// Probe structurally inspects the master at path (container, video/audio codec,
// H.264 profile/level, dimensions, bitrate, duration).
func (Runner) Probe(ctx context.Context, path string) (ProbeResult, error) {
	return Probe(ctx, path)
}

// RemuxProxy copies the master's streams into a faststart mp4 proxy at out
// without re-encoding (used when the master is already browser-compatible).
func (Runner) RemuxProxy(ctx context.Context, in, out string) error {
	return RemuxProxy(ctx, in, out)
}

// RenderProxy transcodes in into a browser proxy at out.
func (Runner) RenderProxy(ctx context.Context, in, out string) error {
	return RenderProxy(ctx, in, out)
}

// ExtractAudio writes a mono 16 kHz FLAC track from in to out.
func (Runner) ExtractAudio(ctx context.Context, in, out string) error {
	return ExtractAudio(ctx, in, out)
}

// Error is a media-processing failure. It exposes a short neutral Op for the
// caller's log message and carries StderrTail — the raw last bytes of the tool's
// stderr — which the caller must keep server-side only, never surface to a
// client. Its Error() string includes the tail on purpose: it is a
// server-log-facing value.
type Error struct {
	Op         string
	StderrTail string
	Err        error
}

func (e *Error) Error() string {
	if e.StderrTail == "" {
		return fmt.Sprintf("media: %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("media: %s: %v: %s", e.Op, e.Err, e.StderrTail)
}

func (e *Error) Unwrap() error { return e.Err }

// ProbeResult is the structured summary of a master, measured by ffprobe. It is
// the single source of an episode's duration (verbatim invariant: we measure,
// never estimate) and the input to the remux/transcode eligibility ruling and
// the server-side probe log line. Absent or unknown fields are the zero value.
//
// Fields mirror ffprobe's format/stream reporting:
//   - Container is the ffprobe format_name list; for mp4 and QuickTime .mov it is
//     "mov,mp4,m4a,3gp,3g2,mj2" (the demuxer these share), for Matroska/WebM it is
//     "matroska,webm".
//   - VideoLevel is ffprobe's integer H.264 level_idc, i.e. the level × 10
//     (level 4.2 -> 42, level 3.1 -> 31); 0 when the stream carries none. The
//     H.264 spec stores level_idc as ten times the level number.
type ProbeResult struct {
	Container      string
	Duration       time.Duration
	OverallBitRate int64 // bits/sec, from format.bit_rate; 0 when ffprobe reports none
	VideoCodec     string
	VideoProfile   string
	VideoLevel     int
	Width          int
	Height         int
	AudioCodec     string
}

// LogValue renders the probe as a flat slog group for the server-side summary
// line. It names codecs and dimensions only — never a provider or model — so it
// is safe for logs, which are a server-side surface.
func (p ProbeResult) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("container", p.Container),
		slog.String("duration", p.Duration.String()),
		slog.Int64("overall_bitrate", p.OverallBitRate),
		slog.String("video_codec", p.VideoCodec),
		slog.String("video_profile", p.VideoProfile),
		slog.Int("video_level", p.VideoLevel),
		slog.Int("width", p.Width),
		slog.Int("height", p.Height),
		slog.String("audio_codec", p.AudioCodec),
	)
}

// Probe structurally inspects the media at path with a single ffprobe pass and
// returns its container/stream summary. Duration comes from the container's
// format=duration (verbatim invariant: measured, never estimated); a master that
// reports no duration is a hard error rather than a guessed zero.
func Probe(ctx context.Context, path string) (ProbeResult, error) {
	if _, err := exec.LookPath(ffprobeBin); err != nil {
		return ProbeResult{}, ErrUnavailable
	}
	// -v error keeps stdout to just the JSON document; -show_format carries the
	// container name, duration, and overall bit rate; -show_streams carries each
	// stream's codec, H.264 profile/level, and dimensions.
	args := []string{
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	}
	out, tail, err := run(ctx, ffprobeBin, args)
	if err != nil {
		return ProbeResult{}, &Error{Op: "probe", StderrTail: tail, Err: err}
	}
	res, perr := parseProbe(out)
	if perr != nil {
		return ProbeResult{}, &Error{Op: "probe", StderrTail: tail, Err: perr}
	}
	if res.Duration <= 0 {
		return ProbeResult{}, &Error{Op: "probe", StderrTail: tail, Err: errors.New("no duration reported")}
	}
	return res, nil
}

// parseProbe extracts a ProbeResult from an ffprobe `-print_format json` document.
// It is pure (no process) so the parser is unit-tested against fixture outputs.
// The first video and first audio stream win; ffprobe emits numeric fields
// (level, width, height) as JSON numbers and duration/bit_rate as strings.
func parseProbe(data []byte) (ProbeResult, error) {
	var doc struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Profile   string `json:"profile"`
			Level     int    `json:"level"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
			BitRate    string `json:"bit_rate"`
		} `json:"format"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return ProbeResult{}, fmt.Errorf("parse ffprobe json: %w", err)
	}
	res := ProbeResult{
		Container:      doc.Format.FormatName,
		Duration:       parseSeconds(doc.Format.Duration),
		OverallBitRate: parseBitRate(doc.Format.BitRate),
	}
	for _, s := range doc.Streams {
		switch s.CodecType {
		case "video":
			if res.VideoCodec == "" {
				res.VideoCodec = s.CodecName
				res.VideoProfile = s.Profile
				res.VideoLevel = s.Level
				res.Width = s.Width
				res.Height = s.Height
			}
		case "audio":
			if res.AudioCodec == "" {
				res.AudioCodec = s.CodecName
			}
		}
	}
	return res, nil
}

// parseSeconds converts an ffprobe fractional-seconds string to a Duration,
// yielding 0 for the absent/"N/A"/malformed cases (Probe rejects a zero
// duration, so a missing value never silently becomes a guessed length).
func parseSeconds(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0
	}
	secs, err := strconv.ParseFloat(s, 64)
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}

// parseBitRate converts an ffprobe bit_rate string to bits/sec, yielding 0 for
// the absent/"N/A"/malformed cases (an unknown overall bitrate disqualifies the
// remux fast path, which errs toward re-encoding).
func parseBitRate(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// Remux-eligibility bounds. A master that satisfies all of these is already a
// browser-playable stream, so the proxy is produced by a stream copy (RemuxProxy)
// instead of a full transcode (RenderProxy).
//
// Compatibility rules are grounded in documented browser support, not invented:
//
//   - H.264 (AVC) is the most broadly compatible <video> codec, and the browser
//     -safe H.264 profiles are the 8-bit 4:2:0 set: Constrained Baseline, Baseline,
//     Main, and High. The higher profiles (High 10, High 4:2:2, High 4:4:4
//     Predictive) carry 10/12-bit or higher chroma subsampling that browser
//     decoders do not universally support, so they fall to the transcode path.
//     Ref: MDN Web Docs, "Web video codec guide" and the H.264 (AVC) codec page
//     (developer.mozilla.org/en-US/docs/Web/Media/Formats/Video_codecs).
//   - Level is capped at 4.2 (level_idc 42). Our proxies never exceed 1080p, for
//     which level 4.x is sufficient and universally decodable; a higher level
//     implies a resolution/framerate/bitrate envelope we would rather re-encode.
//     Ref: MDN H.264 profile/level notes; H.264 spec (level_idc = level × 10).
//   - Audio must be AAC — the audio codec MP4/browser pipelines pair with H.264.
//     Ref: MDN "Web audio codec guide" (AAC in MP4 is the broad-support baseline).
//   - Container must be MP4 or QuickTime MOV (ffprobe: "mov,mp4,m4a,3gp,3g2,mj2").
//   - Height must be ≤ 1080; taller masters are downscaled by the transcode path.
//   - Overall bitrate must be ≤ the caller's budget (config PROXY_MAX_REMUX_BITRATE)
//     so a remuxed proxy still streams cheaply; an unknown bitrate disqualifies.
const (
	// h264RemuxMaxLevel is level 4.2 as ffprobe's integer level_idc (level × 10).
	h264RemuxMaxLevel = 42
	// proxyRemuxMaxHeight caps a remuxed proxy at 1080p.
	proxyRemuxMaxHeight = 1080
)

// h264BrowserProfiles is the set of H.264 profiles a browser <video> element
// decodes reliably (8-bit 4:2:0). Anything outside it re-encodes. See the
// eligibility block above for the MDN reference.
var h264BrowserProfiles = map[string]bool{
	"Constrained Baseline": true,
	"Baseline":             true,
	"Main":                 true,
	"High":                 true,
}

// RemuxDecision is the outcome of the eligibility ruling. Reason is a
// server-log-only explanation (it names codecs/dimensions, never a provider).
type RemuxDecision struct {
	Remux  bool
	Reason string
}

// EligibleForRemux rules whether the probed master can become a proxy by stream
// copy (remux) rather than a full transcode. maxOverallBitRate is the caller's
// bit/sec budget for a remuxed proxy (config PROXY_MAX_REMUX_BITRATE). The first
// failing condition is reported so the log line explains the ruling.
func EligibleForRemux(p ProbeResult, maxOverallBitRate int64) RemuxDecision {
	switch {
	case p.VideoCodec != "h264":
		return RemuxDecision{false, fmt.Sprintf("video codec %q is not h264", p.VideoCodec)}
	case !h264BrowserProfiles[p.VideoProfile]:
		return RemuxDecision{false, fmt.Sprintf("h264 profile %q is above browser-safe High", p.VideoProfile)}
	case p.VideoLevel <= 0 || p.VideoLevel > h264RemuxMaxLevel:
		return RemuxDecision{false, fmt.Sprintf("h264 level %d is unknown or above 4.2", p.VideoLevel)}
	case p.AudioCodec != "aac":
		return RemuxDecision{false, fmt.Sprintf("audio codec %q is not aac", p.AudioCodec)}
	case !containerIsMP4orMOV(p.Container):
		return RemuxDecision{false, fmt.Sprintf("container %q is not mp4/mov", p.Container)}
	case p.Height > proxyRemuxMaxHeight:
		return RemuxDecision{false, fmt.Sprintf("height %d is above %d", p.Height, proxyRemuxMaxHeight)}
	case p.OverallBitRate <= 0:
		return RemuxDecision{false, "overall bitrate is unknown"}
	case p.OverallBitRate > maxOverallBitRate:
		return RemuxDecision{false, fmt.Sprintf("overall bitrate %d exceeds budget %d", p.OverallBitRate, maxOverallBitRate)}
	default:
		return RemuxDecision{true, "h264/aac mp4 within browser-safe profile, level, height, and bitrate"}
	}
}

// containerIsMP4orMOV reports whether ffprobe's comma-separated format_name list
// includes the mp4 or QuickTime mov demuxer token (both report the shared
// "mov,mp4,m4a,3gp,3g2,mj2" family).
func containerIsMP4orMOV(formatName string) bool {
	for _, f := range strings.Split(formatName, ",") {
		if f == "mp4" || f == "mov" {
			return true
		}
	}
	return false
}

// RemuxProxy produces the proxy by copying the master's first video and audio
// streams into a faststart mp4 — no re-encode — for a master the eligibility
// ruling found already browser-compatible (EligibleForRemux). This is the fast
// path: a stream copy runs in seconds where a transcode runs in minutes.
//
//   - `-c copy` is ffmpeg's stream-copy mode: it muxes the encoded packets
//     through unchanged, bit-for-bit, rather than decoding and re-encoding.
//     Ref: ffmpeg(1), "Stream copy" / "-codec copy".
//   - `-movflags +faststart` runs the mp4 muxer's second pass that relocates the
//     moov atom ahead of mdat so the file streams before it is fully downloaded.
//     Ref: ffmpeg-formats(5), mov/mp4/ismv muxer options.
//
// Only the first video and first audio stream are mapped (matching what the
// transcode path emits); other streams (data/timecode/subtitles) are dropped so
// an unsupported stream can never fail the mp4 mux.
func RemuxProxy(ctx context.Context, in, out string) error {
	if _, err := exec.LookPath(ffmpegBin); err != nil {
		return ErrUnavailable
	}
	args := []string{
		"-nostdin", "-y",
		"-i", in,
		"-map", "0:v:0", "-map", "0:a:0?",
		"-c", "copy",
		"-movflags", "+faststart",
		out,
	}
	if _, tail, err := run(ctx, ffmpegBin, args); err != nil {
		return &Error{Op: "remux proxy", StderrTail: tail, Err: err}
	}
	return nil
}

// RenderProxy transcodes in into a browser-playable proxy at out: H.264 (high
// profile, yuv420p) scaled to at most 720px tall with the aspect ratio and even
// dimensions preserved, AAC audio, and the moov atom relocated to the front
// (+faststart) so the file streams before it is fully downloaded. This is the
// fallback path for a master the remux ruling rejected (EligibleForRemux).
//
//   - `-preset veryfast`: the x264 preset trades compression efficiency for
//     encode speed at a *fixed* quality target (CRF is held), so veryfast yields a
//     somewhat larger file than the default `medium` at visually equivalent
//     quality — an acceptable trade for a disposable, regenerable proxy (not the
//     deliverable) that must ingest fast on a modest worker. Ref: FFmpeg H.264
//     encoding guide (trac.ffmpeg.org/wiki/Encode/H.264) and x264 --fullhelp
//     preset scale.
//   - `-crf 20`: constant-quality rate control, unchanged, so the preset change
//     affects filesize/speed, not perceived quality. Ref: same guide, "CRF".
//   - `-threads 0`: let libx264 pick the thread count from the available cores
//     (0 = automatic) so the multi-vCPU worker is fully used. Ref: ffmpeg(1)
//     "-threads", x264 "threads".
func RenderProxy(ctx context.Context, in, out string) error {
	if _, err := exec.LookPath(ffmpegBin); err != nil {
		return ErrUnavailable
	}
	args := []string{
		"-nostdin", "-y",
		"-i", in,
		// Cap height at 720, never upscale; -2 keeps width even for yuv420p.
		"-vf", "scale=-2:'min(ih,720)'",
		"-c:v", "libx264", "-profile:v", "high", "-preset", "veryfast",
		"-crf", "20", "-pix_fmt", "yuv420p",
		"-threads", "0",
		"-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart",
		out,
	}
	if _, tail, err := run(ctx, ffmpegBin, args); err != nil {
		return &Error{Op: "render proxy", StderrTail: tail, Err: err}
	}
	return nil
}

// ExtractAudio writes a mono 16 kHz FLAC track from in to out — the input shape
// M1's ASR stage consumes. No video, lossless within the resampled envelope.
func ExtractAudio(ctx context.Context, in, out string) error {
	if _, err := exec.LookPath(ffmpegBin); err != nil {
		return ErrUnavailable
	}
	args := []string{
		"-nostdin", "-y",
		"-i", in,
		"-vn",
		"-ac", "1", "-ar", "16000",
		"-c:a", "flac",
		out,
	}
	if _, tail, err := run(ctx, ffmpegBin, args); err != nil {
		return &Error{Op: "extract audio", StderrTail: tail, Err: err}
	}
	return nil
}

// run executes bin with args, returning stdout and a bounded tail of stderr.
// The child runs in its own process group and cmd.Cancel kills that whole group
// on context cancellation, so a timed-out or aborted ffmpeg leaves no orphans.
func run(ctx context.Context, bin string, args []string) (stdout []byte, stderrTail string, err error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Kill the entire process group (negative pid) rather than just the leader,
	// so ffmpeg's children die with it.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// After Cancel, give the group a moment to die, then force-reap.
	cmd.WaitDelay = 5 * time.Second

	var outBuf bytes.Buffer
	tail := &tailBuffer{max: stderrTailBytes}
	cmd.Stdout = &outBuf
	cmd.Stderr = tail

	runErr := cmd.Run()
	// Prefer the context error so a timeout is reported as a timeout, not as the
	// SIGKILL exit status the group kill produces.
	if ctx.Err() != nil {
		return outBuf.Bytes(), tail.String(), ctx.Err()
	}
	return outBuf.Bytes(), tail.String(), runErr
}

// tailBuffer is an io.Writer that keeps only the last max bytes written. It
// bounds stderr capture for a verbose tool without discarding the tail, which is
// where the actual error message lives.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}
