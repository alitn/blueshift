package pipeline

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"blueshift/internal/blob"
	"blueshift/internal/media"
)

// These are full-stack ingest runs: the real pipeline.Runner over the real
// internal/media ffmpeg wrappers and the real internal/blob filesystem store
// (direct-path mode), with only the Postgres-backed Repo replaced by the
// in-memory fake. They skip cleanly when ffmpeg is absent. They double as the
// task's acceptance evidence — `go test -run TestIngestReal -v ./internal/pipeline`
// prints the status transition, proxy key, and measured duration.

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if !media.Available() {
		t.Skip("skip: ffmpeg/ffprobe not on PATH (real-media ingest exercised only where present)")
	}
}

// genMaster writes a 2s H.264+AAC clip at path using ffmpeg directly.
func genMaster(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir master: %v", err)
	}
	cmd := exec.Command("ffmpeg", "-nostdin", "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("generate master: %v: %s", err, stderr.String())
	}
}

// genMasterSpec writes an H.264+AAC master with an explicit size/profile/level so
// a real-ffmpeg test can control remux-eligibility (a 1080p High master is
// remux-eligible). Uses ffmpeg directly, not the wrappers under test.
func genMasterSpec(t *testing.T, path, size, profile, level string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir master: %v", err)
	}
	cmd := exec.Command("ffmpeg", "-nostdin", "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=2:size="+size+":rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:v", "libx264", "-profile:v", profile, "-level", level, "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("generate master: %v: %s", err, stderr.String())
	}
}

func probe(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("ffprobe", args...).Output()
	if err != nil {
		t.Fatalf("ffprobe %v: %v", args, err)
	}
	return string(bytes.TrimSpace(out))
}

func TestIngestRealHappyPath(t *testing.T) {
	requireFFmpeg(t)

	blobDir := t.TempDir()
	local, err := blob.NewLocal(blobDir, []byte("secret"), nil)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}

	// Build the org-owned master key exactly as m0-upload does, and drop a real
	// 2s master at that key under the store root.
	org := "org_0000000000000000000000000a"
	ep := "ep_00000000000000000000000000a"
	masterKey, err := blob.MasterKey(org, ep, "master.mp4")
	if err != nil {
		t.Fatalf("MasterKey: %v", err)
	}
	genMaster(t, filepath.Join(blobDir, masterKey))

	repo := newFakeRepo()
	repo.add(ep, org, masterKey)
	r := &Runner{Repo: repo, Blob: local, Media: media.Runner{}, Log: discard(), Config: Config{Retries: 2}}

	t.Logf("[ingest] episode=%s status=uploaded master=%s", ep, masterKey)
	if err := r.Run(context.Background(), ep, "ingest"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	e := repo.get(ep)
	t.Logf("[ingest] status=%s proxy_object_key=%s duration_ms=%d", e.status, e.proxyKey, e.durationMs)

	if e.status != "ready" {
		t.Errorf("status = %q, want ready", e.status)
	}
	if d := e.durationMs - 2000; d < -50 || d > 50 {
		t.Errorf("duration_ms = %d, want ~2000 (±50)", e.durationMs)
	}

	// The proxy and audio landed under proxies/ and are real, playable renders.
	proxyPath := filepath.Join(blobDir, e.proxyKey)
	audioPath := filepath.Join(blobDir, org, ep, "proxies", audioFilename)
	if _, err := os.Stat(proxyPath); err != nil {
		t.Fatalf("proxy missing: %v", err)
	}
	vcodec := probe(t, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name", "-of", "csv=p=0", proxyPath)
	acodec := probe(t, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name", "-of", "csv=p=0", proxyPath)
	arate := probe(t, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=sample_rate", "-of", "csv=p=0", audioPath)
	t.Logf("[ingest] proxy: video=%s audio=%s | audio.flac sample_rate=%s | faststart=%v",
		vcodec, acodec, arate, faststartFront(t, proxyPath))
	if vcodec != "h264" || acodec != "aac" {
		t.Errorf("proxy codecs = %s/%s, want h264/aac", vcodec, acodec)
	}
	if arate != "16000" {
		t.Errorf("audio.flac sample_rate = %s, want 16000", arate)
	}
	if !faststartFront(t, proxyPath) {
		t.Error("proxy not faststart")
	}
}

func TestIngestRealFailurePath(t *testing.T) {
	requireFFmpeg(t)

	blobDir := t.TempDir()
	local, err := blob.NewLocal(blobDir, []byte("secret"), nil)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	org := "org_0000000000000000000000000b"
	ep := "ep_00000000000000000000000000b"
	masterKey, err := blob.MasterKey(org, ep, "master.mp4")
	if err != nil {
		t.Fatalf("MasterKey: %v", err)
	}
	// A zero-byte master: ffprobe/ffmpeg fail on it every attempt.
	if err := os.MkdirAll(filepath.Join(blobDir, org, ep, "masters"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(blobDir, masterKey), nil, 0o600); err != nil {
		t.Fatalf("write empty master: %v", err)
	}

	repo := newFakeRepo()
	repo.add(ep, org, masterKey)
	r := &Runner{Repo: repo, Blob: local, Media: media.Runner{}, Log: discard(), Config: Config{Retries: 2}}

	t.Logf("[ingest-fail] episode=%s status=uploaded master=<zero bytes>", ep)
	start := time.Now()
	err = r.Run(context.Background(), ep, "ingest")
	e := repo.get(ep)
	t.Logf("[ingest-fail] status=%s error_id=%s elapsed=%s returned_error=%q",
		e.status, e.errorID, time.Since(start).Round(time.Millisecond), err)

	if e.status != "failed" {
		t.Errorf("status = %q, want failed", e.status)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(e.errorID) {
		t.Errorf("error_id = %q, want neutral 16-hex id", e.errorID)
	}
	// The client-facing error names no tool/provider — only the neutral id.
	if err == nil || !regexp.MustCompile(`error_id=[0-9a-f]{16}`).MatchString(err.Error()) {
		t.Errorf("returned error = %v, want neutral error_id", err)
	}
}

// TestIngestRealRemuxPath drives a full ingest over real ffmpeg with a master
// that is already browser-compatible: the pipeline must take the remux
// (stream-copy) fast path. The proof is that the proxy's video stream is copied
// verbatim — codecs preserved AND the full 1080p height retained (a transcode
// would have downscaled to ≤720). It shares the transcoded proxy's playability
// assertions (h264/aac, faststart, 16 kHz audio.flac).
func TestIngestRealRemuxPath(t *testing.T) {
	requireFFmpeg(t)

	blobDir := t.TempDir()
	local, err := blob.NewLocal(blobDir, []byte("secret"), nil)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	org := "org_0000000000000000000000000c"
	ep := "ep_00000000000000000000000000c"
	masterKey, err := blob.MasterKey(org, ep, "master.mp4")
	if err != nil {
		t.Fatalf("MasterKey: %v", err)
	}
	// 1080p H.264 High L4.2 AAC mp4: satisfies every remux rule.
	genMasterSpec(t, filepath.Join(blobDir, masterKey), "1920x1080", "high", "4.2")

	repo := newFakeRepo()
	repo.add(ep, org, masterKey)
	r := &Runner{Repo: repo, Blob: local, Media: media.Runner{}, Log: discard(), Config: Config{Retries: 2}}

	if err := r.Run(context.Background(), ep, "ingest"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	e := repo.get(ep)
	if e.status != "ready" {
		t.Fatalf("status = %q, want ready", e.status)
	}
	proxyPath := filepath.Join(blobDir, e.proxyKey)
	audioPath := filepath.Join(blobDir, org, ep, "proxies", audioFilename)

	vcodec := probe(t, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name", "-of", "csv=p=0", proxyPath)
	height := probe(t, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=height", "-of", "csv=p=0", proxyPath)
	acodec := probe(t, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name", "-of", "csv=p=0", proxyPath)
	arate := probe(t, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=sample_rate", "-of", "csv=p=0", audioPath)
	t.Logf("[remux] proxy: video=%s height=%s audio=%s | audio.flac sample_rate=%s | faststart=%v",
		vcodec, height, acodec, arate, faststartFront(t, proxyPath))

	if vcodec != "h264" || acodec != "aac" {
		t.Errorf("proxy codecs = %s/%s, want h264/aac", vcodec, acodec)
	}
	// The distinguishing signal: a stream copy retains the master's 1080p height.
	if height != "1080" {
		t.Errorf("proxy height = %s, want 1080 (stream copy, not the ≤720 transcode)", height)
	}
	if arate != "16000" {
		t.Errorf("audio.flac sample_rate = %s, want 16000", arate)
	}
	if !faststartFront(t, proxyPath) {
		t.Error("remuxed proxy not faststart")
	}
}

// TestIngestRealTranscodePath drives a full ingest over real ffmpeg down the
// transcode fallback, using the SAME eligible master but a 1-bit remux budget to
// force it — proving the ruling is config-tunable and the transcode path still
// yields a playable, downscaled (≤720) H.264 proxy.
func TestIngestRealTranscodePath(t *testing.T) {
	requireFFmpeg(t)

	blobDir := t.TempDir()
	local, err := blob.NewLocal(blobDir, []byte("secret"), nil)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	org := "org_0000000000000000000000000d"
	ep := "ep_00000000000000000000000000d"
	masterKey, err := blob.MasterKey(org, ep, "master.mp4")
	if err != nil {
		t.Fatalf("MasterKey: %v", err)
	}
	genMasterSpec(t, filepath.Join(blobDir, masterKey), "1920x1080", "high", "4.2")

	repo := newFakeRepo()
	repo.add(ep, org, masterKey)
	// A 1 bit/sec remux budget rejects even this compatible master -> transcode.
	r := &Runner{Repo: repo, Blob: local, Media: media.Runner{}, Log: discard(),
		Config: Config{Retries: 2, MaxRemuxBitrate: 1}}

	if err := r.Run(context.Background(), ep, "ingest"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	e := repo.get(ep)
	if e.status != "ready" {
		t.Fatalf("status = %q, want ready", e.status)
	}
	proxyPath := filepath.Join(blobDir, e.proxyKey)
	audioPath := filepath.Join(blobDir, org, ep, "proxies", audioFilename)

	vcodec := probe(t, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name", "-of", "csv=p=0", proxyPath)
	hs := probe(t, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=height", "-of", "csv=p=0", proxyPath)
	arate := probe(t, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=sample_rate", "-of", "csv=p=0", audioPath)
	t.Logf("[transcode] proxy: video=%s height=%s | audio.flac sample_rate=%s | faststart=%v",
		vcodec, hs, arate, faststartFront(t, proxyPath))

	h, err := strconv.Atoi(hs)
	if err != nil {
		t.Fatalf("parse height %q: %v", hs, err)
	}
	if vcodec != "h264" {
		t.Errorf("proxy video codec = %q, want h264", vcodec)
	}
	if h > 720 {
		t.Errorf("proxy height = %d, want ≤720 (transcode downscale)", h)
	}
	if arate != "16000" {
		t.Errorf("audio.flac sample_rate = %s, want 16000", arate)
	}
	if !faststartFront(t, proxyPath) {
		t.Error("transcoded proxy not faststart")
	}
}

// faststartFront reports whether moov precedes mdat near the file head.
func faststartFront(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 1<<18)
	n, _ := f.Read(buf)
	buf = buf[:n]
	moov := bytes.Index(buf, []byte("moov"))
	mdat := bytes.Index(buf, []byte("mdat"))
	return moov >= 0 && (mdat < 0 || moov < mdat)
}
