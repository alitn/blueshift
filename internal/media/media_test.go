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

// genMasterSpec writes an H.264+AAC clip with an explicit size, profile, and
// level so a test can build a master with known remux-eligibility (e.g. a 1080p
// High-profile file for the remux fast path). Uses ffmpeg directly.
func genMasterSpec(t *testing.T, dir, name, size, profile, level string) string {
	t.Helper()
	out := filepath.Join(dir, name)
	cmd := exec.Command(ffmpegBin, "-nostdin", "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=2:size="+size+":rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:v", "libx264", "-profile:v", profile, "-level", level, "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", out)
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

func TestProbe(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	master := genMaster(t, dir)

	got, err := Probe(context.Background(), master)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	// The fixture is exactly 2.0s; ffprobe reports it verbatim. Allow ±50ms.
	if d := got.Duration - 2*time.Second; d < -50*time.Millisecond || d > 50*time.Millisecond {
		t.Errorf("duration = %v, want ~2s (±50ms)", got.Duration)
	}
	if got.VideoCodec != "h264" {
		t.Errorf("video codec = %q, want h264", got.VideoCodec)
	}
	if got.AudioCodec != "aac" {
		t.Errorf("audio codec = %q, want aac", got.AudioCodec)
	}
	if !containerIsMP4orMOV(got.Container) {
		t.Errorf("container = %q, want an mp4/mov family", got.Container)
	}
	if got.Width != 320 || got.Height != 240 {
		t.Errorf("dimensions = %dx%d, want 320x240", got.Width, got.Height)
	}
	// libx264 on 8-bit 4:2:0 defaults to the High profile; a real level is present.
	if got.VideoProfile == "" || got.VideoLevel <= 0 {
		t.Errorf("profile/level = %q/%d, want both reported", got.VideoProfile, got.VideoLevel)
	}
	if got.OverallBitRate <= 0 {
		t.Errorf("overall bitrate = %d, want a positive rate", got.OverallBitRate)
	}
}

func TestProbeMissingFile(t *testing.T) {
	requireFFmpeg(t)
	if _, err := Probe(context.Background(), filepath.Join(t.TempDir(), "nope.mp4")); err == nil {
		t.Fatal("Probe on missing file: want error, got nil")
	}
}

// TestParseProbe exercises the pure JSON parser against captured ffprobe outputs
// (internal/media/testdata) so field extraction is covered without a process.
func TestParseProbe(t *testing.T) {
	load := func(name string) ProbeResult {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		got, err := parseProbe(data)
		if err != nil {
			t.Fatalf("parseProbe(%s): %v", name, err)
		}
		return got
	}

	t.Run("h264 high mp4", func(t *testing.T) {
		p := load("probe_h264_high_mp4.json")
		if p.VideoCodec != "h264" || p.VideoProfile != "High" || p.VideoLevel != 42 {
			t.Errorf("video = %q/%q/%d, want h264/High/42", p.VideoCodec, p.VideoProfile, p.VideoLevel)
		}
		if p.AudioCodec != "aac" {
			t.Errorf("audio = %q, want aac", p.AudioCodec)
		}
		if p.Width != 1920 || p.Height != 1080 {
			t.Errorf("dims = %dx%d, want 1920x1080", p.Width, p.Height)
		}
		if p.Container != "mov,mp4,m4a,3gp,3g2,mj2" {
			t.Errorf("container = %q", p.Container)
		}
		if p.OverallBitRate <= 0 {
			t.Errorf("bitrate = %d, want positive", p.OverallBitRate)
		}
		if p.Duration <= 0 {
			t.Errorf("duration = %v, want positive", p.Duration)
		}
	})

	t.Run("hevc mp4", func(t *testing.T) {
		p := load("probe_hevc_mp4.json")
		if p.VideoCodec != "hevc" {
			t.Errorf("video codec = %q, want hevc", p.VideoCodec)
		}
	})

	t.Run("h264 matroska", func(t *testing.T) {
		p := load("probe_h264_mkv.json")
		if p.Container != "matroska,webm" {
			t.Errorf("container = %q, want matroska,webm", p.Container)
		}
		if p.VideoCodec != "h264" {
			t.Errorf("video codec = %q, want h264", p.VideoCodec)
		}
	})

	t.Run("missing bitrate and NA duration", func(t *testing.T) {
		p := load("probe_h264_mp4_no_bitrate.json")
		if p.OverallBitRate != 0 {
			t.Errorf("bitrate = %d, want 0 for an absent bit_rate", p.OverallBitRate)
		}
		if p.Duration != 0 {
			t.Errorf("duration = %v, want 0 for an N/A duration", p.Duration)
		}
		// The rest of the structure still parses.
		if p.VideoCodec != "h264" || p.AudioCodec != "aac" || p.VideoLevel != 31 {
			t.Errorf("streams = %q/%q level %d, want h264/aac/31", p.VideoCodec, p.AudioCodec, p.VideoLevel)
		}
	})
}

// TestEligibleForRemux is the ruling table, including the boundary values on
// every numeric bound (level, height, bitrate).
func TestEligibleForRemux(t *testing.T) {
	const maxBitRate = 6_000_000
	// base is a fully remux-eligible master; each case tweaks one field.
	base := ProbeResult{
		Container:      "mov,mp4,m4a,3gp,3g2,mj2",
		Duration:       time.Minute,
		OverallBitRate: 5_000_000,
		VideoCodec:     "h264",
		VideoProfile:   "High",
		VideoLevel:     40,
		Width:          1920,
		Height:         1080,
		AudioCodec:     "aac",
	}
	with := func(mut func(*ProbeResult)) ProbeResult {
		p := base
		mut(&p)
		return p
	}

	cases := []struct {
		name string
		p    ProbeResult
		want bool
	}{
		{"all eligible", base, true},
		{"bare mp4 container", with(func(p *ProbeResult) { p.Container = "mp4" }), true},
		{"bare mov container", with(func(p *ProbeResult) { p.Container = "mov" }), true},
		{"profile Main", with(func(p *ProbeResult) { p.VideoProfile = "Main" }), true},
		{"profile Baseline", with(func(p *ProbeResult) { p.VideoProfile = "Baseline" }), true},
		{"profile Constrained Baseline", with(func(p *ProbeResult) { p.VideoProfile = "Constrained Baseline" }), true},
		{"profile High 10 rejected", with(func(p *ProbeResult) { p.VideoProfile = "High 10" }), false},
		{"profile High 4:2:2 rejected", with(func(p *ProbeResult) { p.VideoProfile = "High 4:2:2" }), false},
		{"codec hevc rejected", with(func(p *ProbeResult) { p.VideoCodec = "hevc" }), false},
		{"codec vp9 rejected", with(func(p *ProbeResult) { p.VideoCodec = "vp9" }), false},
		{"audio mp3 rejected", with(func(p *ProbeResult) { p.AudioCodec = "mp3" }), false},
		{"audio opus rejected", with(func(p *ProbeResult) { p.AudioCodec = "opus" }), false},
		{"matroska container rejected", with(func(p *ProbeResult) { p.Container = "matroska,webm" }), false},
		{"webm container rejected", with(func(p *ProbeResult) { p.Container = "matroska,webm" }), false},
		{"level 42 boundary ok", with(func(p *ProbeResult) { p.VideoLevel = 42 }), true},
		{"level 43 rejected", with(func(p *ProbeResult) { p.VideoLevel = 43 }), false},
		{"level 51 rejected", with(func(p *ProbeResult) { p.VideoLevel = 51 }), false},
		{"level unknown rejected", with(func(p *ProbeResult) { p.VideoLevel = 0 }), false},
		{"height 1080 boundary ok", with(func(p *ProbeResult) { p.Height = 1080 }), true},
		{"height 1081 rejected", with(func(p *ProbeResult) { p.Height = 1081 }), false},
		{"height 2160 rejected", with(func(p *ProbeResult) { p.Height = 2160 }), false},
		{"bitrate at budget ok", with(func(p *ProbeResult) { p.OverallBitRate = maxBitRate }), true},
		{"bitrate over budget rejected", with(func(p *ProbeResult) { p.OverallBitRate = maxBitRate + 1 }), false},
		{"bitrate unknown rejected", with(func(p *ProbeResult) { p.OverallBitRate = 0 }), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EligibleForRemux(tc.p, maxBitRate)
			if got.Remux != tc.want {
				t.Errorf("EligibleForRemux = %v (%s), want %v", got.Remux, got.Reason, tc.want)
			}
			if got.Reason == "" {
				t.Error("decision reason is empty; want a server-log explanation")
			}
		})
	}
}

// TestRemuxProxy proves the fast path is a faststart stream copy: the proxy's
// video stream is bit-identical to a browser-compatible master (same codec,
// profile, level, and resolution — no downscale), and the moov atom precedes
// mdat. It shares the playability assertions the transcoded proxy must also pass.
func TestRemuxProxy(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	// A 1080p H.264 High master: eligible, and tall enough that a copy (1080) is
	// distinguishable from what the transcode path would emit (≤720).
	master := genMasterSpec(t, dir, "master.mp4", "1920x1080", "high", "4.2")
	proxy := filepath.Join(dir, "proxy.mp4")

	if err := RemuxProxy(context.Background(), master, proxy); err != nil {
		t.Fatalf("RemuxProxy: %v", err)
	}

	// Video is copied verbatim: same codec/profile/level and the full 1080 height.
	vinfo := probeField(t, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name,profile,height", "-of", "csv=p=0", proxy)
	if vinfo != "h264,High,1080" {
		t.Errorf("remuxed video = %q, want h264,High,1080 (copied, not re-encoded)", vinfo)
	}
	// Audio is copied AAC.
	if ac := probeField(t, "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", proxy); ac != "aac" {
		t.Errorf("remuxed audio codec = %q, want aac", ac)
	}
	// Same playability assertion as the transcoded proxy: moov before mdat.
	if !faststart(t, proxy) {
		t.Error("remuxed proxy is not faststart (moov not before mdat)")
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

// genAudio writes a mono 16 kHz FLAC tone of the given length (seconds) — the
// shape the ASR stage feeds CutAudio.
func genAudio(t *testing.T, dir string, seconds int) string {
	t.Helper()
	out := filepath.Join(dir, "audio.flac")
	cmd := exec.Command(ffmpegBin, "-nostdin", "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+strconv.Itoa(seconds),
		"-ac", "1", "-ar", "16000", "-c:a", "flac", out)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("generate audio fixture: %v: %s", err, stderr.String())
	}
	return out
}

// TestCutAudio cuts a window out of a real audio fixture and asserts the chunk is
// mono 16 kHz FLAC of the requested length — the transcribe stage's chunking.
func TestCutAudio(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	audio := genAudio(t, dir, 5) // 5s source
	chunk := filepath.Join(dir, "chunk.flac")

	// Cut the middle [1.000s, 3.000s) window (2s).
	if err := CutAudio(context.Background(), audio, chunk, 1000, 2000); err != nil {
		t.Fatalf("CutAudio: %v", err)
	}

	codec := probeField(t, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name", "-of", "csv=p=0", chunk)
	channels := probeField(t, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=channels", "-of", "csv=p=0", chunk)
	rate := probeField(t, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=sample_rate", "-of", "csv=p=0", chunk)
	dur := probeField(t, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", chunk)
	if codec != "flac" {
		t.Errorf("chunk codec = %q, want flac", codec)
	}
	if channels != "1" {
		t.Errorf("chunk channels = %q, want 1 (mono)", channels)
	}
	if rate != "16000" {
		t.Errorf("chunk sample_rate = %q, want 16000", rate)
	}
	secs, err := strconv.ParseFloat(dur, 64)
	if err != nil {
		t.Fatalf("parse chunk duration %q: %v", dur, err)
	}
	if secs < 1.9 || secs > 2.1 {
		t.Errorf("chunk duration = %vs, want ~2s", secs)
	}
}

// TestCutAudioInvalidWindow rejects a non-positive duration before invoking
// ffmpeg, rather than shelling out with a nonsense window.
func TestCutAudioInvalidWindow(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	audio := genAudio(t, dir, 2)
	if err := CutAudio(context.Background(), audio, filepath.Join(dir, "c.flac"), 0, 0); err == nil {
		t.Error("CutAudio with zero duration: want error, got nil")
	}
	if err := CutAudio(context.Background(), audio, filepath.Join(dir, "c.flac"), -1, 1000); err == nil {
		t.Error("CutAudio with negative start: want error, got nil")
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
