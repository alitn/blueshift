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
	"errors"
	"fmt"
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

// ProbeDuration measures the container duration at path.
func (Runner) ProbeDuration(ctx context.Context, path string) (time.Duration, error) {
	return ProbeDuration(ctx, path)
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

// ProbeDuration returns the container duration of the media at path, measured by
// ffprobe. This is the single source of an episode's duration (verbatim
// invariant: we measure, never estimate).
func ProbeDuration(ctx context.Context, path string) (time.Duration, error) {
	if _, err := exec.LookPath(ffprobeBin); err != nil {
		return 0, ErrUnavailable
	}
	// -v error keeps stdout to just the value; format=duration is the container
	// duration in fractional seconds.
	args := []string{
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	}
	out, tail, err := run(ctx, ffprobeBin, args)
	if err != nil {
		return 0, &Error{Op: "probe duration", StderrTail: tail, Err: err}
	}
	s := strings.TrimSpace(string(out))
	if s == "" || s == "N/A" {
		return 0, &Error{Op: "probe duration", StderrTail: tail, Err: errors.New("no duration reported")}
	}
	secs, perr := strconv.ParseFloat(s, 64)
	if perr != nil {
		return 0, &Error{Op: "probe duration", Err: fmt.Errorf("parse %q: %w", s, perr)}
	}
	if secs < 0 {
		return 0, &Error{Op: "probe duration", Err: fmt.Errorf("negative duration %q", s)}
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// RenderProxy transcodes in into a browser-playable proxy at out: H.264 (high
// profile, yuv420p) scaled to at most 720px tall with the aspect ratio and even
// dimensions preserved, AAC audio, and the moov atom relocated to the front
// (+faststart) so the file streams before it is fully downloaded.
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
