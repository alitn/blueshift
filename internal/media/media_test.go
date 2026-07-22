package media

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// requireFFmpeg skips the test with a logged reason when the media binaries are
// absent, so `make check` is green on a machine without ffmpeg (CI installs it
// in m0-ci-deploy) yet runs for real wherever it is present.
func requireFFmpeg(t *testing.T) {
	t.Helper()
	if !Available() {
		t.Skip("skip: ffmpeg/ffprobe not on PATH (media wrappers exercised only where present)")
	}
}

// genMaster writes a 2s H.264+AAC test clip (testsrc video + a sine tone) so the
// wrappers have a real, deterministic master to transcode. It uses ffmpeg
// directly — not the wrappers under test — to build the fixture.
func genMaster(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "master.mp4")
	cmd := exec.Command(ffmpegBin, "-nostdin", "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", out)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("generate master fixture: %v: %s", err, stderr.String())
	}
	return out
}

// probeField runs ffprobe for a single csv stream/format field.
func probeField(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command(ffprobeBin, args...).Output()
	if err != nil {
		t.Fatalf("ffprobe %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestProbeDuration(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	master := genMaster(t, dir)

	got, err := ProbeDuration(context.Background(), master)
	if err != nil {
		t.Fatalf("ProbeDuration: %v", err)
	}
	// The fixture is exactly 2.0s; ffprobe reports it verbatim. Allow ±50ms.
	const want = 2 * time.Second
	if d := got - want; d < -50*time.Millisecond || d > 50*time.Millisecond {
		t.Errorf("duration = %v, want ~%v (±50ms)", got, want)
	}
}

func TestProbeDurationMissingFile(t *testing.T) {
	requireFFmpeg(t)
	if _, err := ProbeDuration(context.Background(), filepath.Join(t.TempDir(), "nope.mp4")); err == nil {
		t.Fatal("ProbeDuration on missing file: want error, got nil")
	}
}

func TestRenderProxy(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	master := genMaster(t, dir)
	proxy := filepath.Join(dir, "proxy.mp4")

	if err := RenderProxy(context.Background(), master, proxy); err != nil {
		t.Fatalf("RenderProxy: %v", err)
	}

	// Video is H.264, height preserved (≤720) and even.
	vinfo := probeField(t, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name,width,height", "-of", "csv=p=0", proxy)
	parts := strings.Split(vinfo, ",")
	if len(parts) != 3 {
		t.Fatalf("unexpected video probe %q", vinfo)
	}
	if parts[0] != "h264" {
		t.Errorf("proxy video codec = %q, want h264", parts[0])
	}
	h, err := strconv.Atoi(parts[2])
	if err != nil {
		t.Fatalf("parse height %q: %v", parts[2], err)
	}
	if h > 720 {
		t.Errorf("proxy height = %d, want ≤720", h)
	}
	if h%2 != 0 {
		t.Errorf("proxy height = %d, want even (yuv420p)", h)
	}
	// The 240p source must not be upscaled.
	if h != 240 {
		t.Errorf("proxy height = %d, want 240 (aspect preserved, no upscale)", h)
	}

	// Audio is AAC.
	if ac := probeField(t, "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", proxy); ac != "aac" {
		t.Errorf("proxy audio codec = %q, want aac", ac)
	}

	// +faststart relocates the moov atom ahead of mdat so the file streams.
	if !faststart(t, proxy) {
		t.Error("proxy is not faststart (moov not before mdat)")
	}
}

func TestExtractAudio(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	master := genMaster(t, dir)
	audio := filepath.Join(dir, "audio.flac")

	if err := ExtractAudio(context.Background(), master, audio); err != nil {
		t.Fatalf("ExtractAudio: %v", err)
	}

	// Probe each field on its own: ffprobe emits show_entries fields in a fixed
	// internal order, not the requested order, so a combined csv is ambiguous.
	codec := probeField(t, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name", "-of", "csv=p=0", audio)
	channels := probeField(t, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=channels", "-of", "csv=p=0", audio)
	rate := probeField(t, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=sample_rate", "-of", "csv=p=0", audio)
	if codec != "flac" {
		t.Errorf("audio codec = %q, want flac", codec)
	}
	if channels != "1" {
		t.Errorf("audio channels = %q, want 1 (mono)", channels)
	}
	if rate != "16000" {
		t.Errorf("audio sample_rate = %q, want 16000", rate)
	}
}

// TestRenderProxyBadInput asserts a corrupt master yields a media.Error whose
// stderr tail is captured (for server logs), not a silent success.
func TestRenderProxyBadInput(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.mp4")
	if err := os.WriteFile(bad, []byte("not a video"), 0o600); err != nil {
		t.Fatalf("write bad master: %v", err)
	}
	err := RenderProxy(context.Background(), bad, filepath.Join(dir, "out.mp4"))
	if err == nil {
		t.Fatal("RenderProxy on garbage: want error, got nil")
	}
	var merr *Error
	if !errors.As(err, &merr) {
		t.Fatalf("error = %T, want *media.Error", err)
	}
	if merr.StderrTail == "" {
		t.Error("media.Error has empty stderr tail; want the tool's diagnostic captured")
	}
}

// TestRunCancelled asserts a cancelled context aborts a wrapper promptly and is
// reported as the context error (the process group is killed).
func TestRunCancelled(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	master := genMaster(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	start := time.Now()
	err := RenderProxy(ctx, master, filepath.Join(dir, "out.mp4"))
	if err == nil {
		t.Fatal("RenderProxy with cancelled context: want error, got nil")
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("cancelled render took %v; want prompt teardown", time.Since(start))
	}
}

// faststart reports whether the moov atom appears before mdat in the first chunk
// of the file — the observable effect of -movflags +faststart.
func faststart(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 1<<18)
	n, _ := f.Read(buf)
	buf = buf[:n]
	moov := bytes.Index(buf, []byte("moov"))
	mdat := bytes.Index(buf, []byte("mdat"))
	return moov >= 0 && (mdat < 0 || moov < mdat)
}
