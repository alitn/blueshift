package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// --- fake repo: in-memory episodes with the store's status machine ----------

type fakeEp struct {
	org        string // encoded org_ id
	status     string
	masterKey  string
	proxyKey   string
	durationMs int64
	errorID    string
	claims     int
}

type fakeRepo struct {
	mu sync.Mutex
	// markRespectsCtx makes MarkFailed honor context cancellation (return
	// ctx.Err() when the passed ctx is already done). The real store does exactly
	// this via pgx, so it lets a test prove the shutdown path finalizes on a
	// *detached* context — a cancelled ctx would leave the episode stuck.
	markRespectsCtx bool
	eps             map[string]*fakeEp // key: encoded ep_ id
}

func newFakeRepo() *fakeRepo { return &fakeRepo{eps: map[string]*fakeEp{}} }

func (f *fakeRepo) add(epID, org, masterKey string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.eps[epID] = &fakeEp{org: org, status: "uploaded", masterKey: masterKey}
}

func (f *fakeRepo) get(epID string) fakeEp {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.eps[epID]
}

// Claim mirrors ClaimEpisodeForIngest: CAS 'uploaded' -> 'processing'.
func (f *fakeRepo) Claim(_ context.Context, epID string) (Episode, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.eps[epID]
	if !ok || e.status != "uploaded" {
		return Episode{}, false, nil
	}
	e.status = "processing"
	e.claims++
	return Episode{OrgID: e.org, PublicID: epID, MasterObjectKey: e.masterKey}, true, nil
}

// MarkReady mirrors the org-scoped, 'processing'-gated finalizer.
func (f *fakeRepo) MarkReady(_ context.Context, org, epID, proxyKey string, durationMs int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.eps[epID]
	if !ok || e.org != org || e.status != "processing" {
		return nil // no-op: cross-org or lost race
	}
	e.status = "ready"
	e.proxyKey = proxyKey
	e.durationMs = durationMs
	e.errorID = ""
	return nil
}

func (f *fakeRepo) MarkFailed(ctx context.Context, org, epID, errorID string) error {
	if f.markRespectsCtx && ctx.Err() != nil {
		// Mirror pgx: a cancelled context aborts the write. The shutdown path must
		// hand us a live (detached) context or the episode stays 'processing'.
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.eps[epID]
	if !ok || e.org != org || e.status != "processing" {
		return nil
	}
	e.status = "failed"
	e.errorID = errorID
	return nil
}

// EpisodeStatus reports the current status by id (or "" when unknown), matching
// the store's non-org-scoped lookup used only for the not-claimable WARN.
func (f *fakeRepo) EpisodeStatus(_ context.Context, epID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.eps[epID]; ok {
		return e.status, nil
	}
	return "", nil
}

// --- fake media: scripted per-attempt behaviour ------------------------------

type fakeMedia struct {
	mu sync.Mutex
	// renderErrs[i] is the error returned by the i-th RenderProxy call (nil =
	// success). Missing indices default to nil.
	renderErrs []error
	// blockOnCtx makes RenderProxy block until the context is cancelled, then
	// return ctx.Err() — used to exercise the per-attempt timeout kill.
	blockOnCtx bool
	duration   time.Duration
	renders    atomic.Int32
	cancelled  atomic.Int32
}

func (m *fakeMedia) ProbeDuration(_ context.Context, _ string) (time.Duration, error) {
	if m.duration == 0 {
		return 2 * time.Second, nil
	}
	return m.duration, nil
}

func (m *fakeMedia) RenderProxy(ctx context.Context, _, out string) error {
	n := int(m.renders.Add(1)) - 1
	if m.blockOnCtx {
		<-ctx.Done()
		m.cancelled.Add(1)
		return ctx.Err()
	}
	m.mu.Lock()
	var err error
	if n < len(m.renderErrs) {
		err = m.renderErrs[n]
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	// Write a placeholder output so the remote-upload path has bytes to move.
	return os.WriteFile(out, []byte("proxy"), 0o600)
}

func (m *fakeMedia) ExtractAudio(_ context.Context, _, out string) error {
	return os.WriteFile(out, []byte("audio"), 0o600)
}

// --- fake blobs: remote (download/upload) and local (direct path) ------------

// remoteBlob emulates GCS: the pipeline downloads the master and uploads the
// renders. It records the object keys it received.
type remoteBlob struct {
	mu        sync.Mutex
	dir       string
	uploaded  map[string]string // key -> stored path
	downloads int
}

func newRemoteBlob(t *testing.T) *remoteBlob {
	return &remoteBlob{dir: t.TempDir(), uploaded: map[string]string{}}
}

func (b *remoteBlob) Download(_ context.Context, _, destPath string) error {
	b.mu.Lock()
	b.downloads++
	b.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(destPath), 0o750); err != nil {
		return err
	}
	return os.WriteFile(destPath, []byte("master"), 0o600)
}

func (b *remoteBlob) Upload(_ context.Context, key, srcPath, _ string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	dst := filepath.Join(b.dir, filepath.Base(key))
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return err
	}
	b.mu.Lock()
	b.uploaded[key] = dst
	b.mu.Unlock()
	return nil
}

// localBlob emulates the filesystem store: it exposes object keys as paths under
// a root, so the pipeline takes the direct-path branch (no download/upload).
type localBlob struct {
	root string
}

func newLocalBlob(t *testing.T, masterKey string) *localBlob {
	b := &localBlob{root: t.TempDir()}
	// Seed the master so ProbeDuration/RenderProxy have an input path.
	p, _ := b.LocalPath(masterKey)
	_ = os.MkdirAll(filepath.Dir(p), 0o750)
	_ = os.WriteFile(p, []byte("master"), 0o600)
	return b
}

func (b *localBlob) LocalPath(key string) (string, error) { return filepath.Join(b.root, key), nil }
func (b *localBlob) Download(context.Context, string, string) error {
	return errors.New("localBlob: Download must not be called in direct mode")
}
func (b *localBlob) Upload(context.Context, string, string, string) error {
	return errors.New("localBlob: Upload must not be called in direct mode")
}

// ids used across the tests (valid encoded forms are not required by the fakes,
// but the direct-path branch builds keys via blob.ProxyKey which validates
// tokens — so use separator-free strings).
const (
	orgA = "org_a"
	orgB = "org_b"
	epA  = "ep_aaaa"
	epB  = "ep_bbbb"
)

func newRunner(repo Repo, blob Blob, media Media, cfg Config) *Runner {
	return &Runner{Repo: repo, Blob: blob, Media: media, Log: discard(), Config: cfg}
}

// --- tests -------------------------------------------------------------------

func TestIngestHappyPathRemote(t *testing.T) {
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	blob := newRemoteBlob(t)
	media := &fakeMedia{duration: 2 * time.Second}
	r := newRunner(repo, blob, media, Config{Retries: 2})

	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	e := repo.get(epA)
	if e.status != "ready" {
		t.Errorf("status = %q, want ready", e.status)
	}
	if e.durationMs != 2000 {
		t.Errorf("duration_ms = %d, want 2000", e.durationMs)
	}
	wantProxy := orgA + "/" + epA + "/proxies/" + proxyFilename
	if e.proxyKey != wantProxy {
		t.Errorf("proxy key = %q, want %q", e.proxyKey, wantProxy)
	}
	if blob.downloads != 1 {
		t.Errorf("downloads = %d, want 1", blob.downloads)
	}
	// Both the proxy and audio outputs were uploaded under proxies/.
	if _, ok := blob.uploaded[wantProxy]; !ok {
		t.Error("proxy not uploaded")
	}
	if _, ok := blob.uploaded[orgA+"/"+epA+"/proxies/"+audioFilename]; !ok {
		t.Error("audio not uploaded")
	}
}

func TestIngestHappyPathLocalDirect(t *testing.T) {
	masterKey := orgA + "/" + epA + "/masters/m.mp4"
	repo := newFakeRepo()
	repo.add(epA, orgA, masterKey)
	blob := newLocalBlob(t, masterKey)
	media := &fakeMedia{duration: 2 * time.Second}
	r := newRunner(repo, blob, media, Config{Retries: 2})

	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	e := repo.get(epA)
	if e.status != "ready" {
		t.Errorf("status = %q, want ready", e.status)
	}
	// The proxy render was written in place under the store root.
	proxyPath := filepath.Join(blob.root, e.proxyKey)
	if _, err := os.Stat(proxyPath); err != nil {
		t.Errorf("proxy not written in place at %s: %v", proxyPath, err)
	}
}

func TestIngestRetryThenSuccess(t *testing.T) {
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	blob := newRemoteBlob(t)
	// First attempt fails to render; second succeeds.
	media := &fakeMedia{renderErrs: []error{errors.New("transient render fault")}}
	r := newRunner(repo, blob, media, Config{Retries: 2})

	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if e := repo.get(epA); e.status != "ready" {
		t.Errorf("status = %q, want ready after retry", e.status)
	}
	if got := media.renders.Load(); got != 2 {
		t.Errorf("render attempts = %d, want 2 (fail then success)", got)
	}
}

func TestIngestRetriesExhaustedFails(t *testing.T) {
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	blob := newRemoteBlob(t)
	// Every attempt fails; with 2 retries that is 3 attempts.
	media := &fakeMedia{renderErrs: []error{
		errors.New("boom 1"), errors.New("boom 2"), errors.New("boom 3"), errors.New("boom 4"),
	}}
	r := newRunner(repo, blob, media, Config{Retries: 2})

	err := r.Run(context.Background(), epA, "ingest")
	if !errors.Is(err, ErrStageFailed) {
		t.Fatalf("Run err = %v, want ErrStageFailed", err)
	}
	if got := media.renders.Load(); got != 3 {
		t.Errorf("render attempts = %d, want 3 (1 + 2 retries)", got)
	}
	e := repo.get(epA)
	if e.status != "failed" {
		t.Errorf("status = %q, want failed", e.status)
	}
	// The recorded error_id is a neutral random hex id — no provider/tool text,
	// and none of the raw render errors leaked into it.
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(e.errorID) {
		t.Errorf("error_id = %q, want 16 hex chars", e.errorID)
	}
	// The returned error carries the same id and nothing about the cause.
	if !regexp.MustCompile(`error_id=[0-9a-f]{16}`).MatchString(err.Error()) {
		t.Errorf("returned error = %q, want a neutral error_id", err.Error())
	}
	for _, leak := range []string{"boom", "render", "ffmpeg"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("returned error %q leaked %q", err.Error(), leak)
		}
	}
}

func TestIngestTimeoutKillsAttempt(t *testing.T) {
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	blob := newRemoteBlob(t)
	media := &fakeMedia{blockOnCtx: true}
	// Tiny per-attempt timeout; 2 retries -> 3 short attempts.
	r := newRunner(repo, blob, media, Config{StageTimeout: 20 * time.Millisecond, Retries: 2})

	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background(), epA, "ingest") }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrStageFailed) {
			t.Fatalf("Run err = %v, want ErrStageFailed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return; timeout did not kill the attempt")
	}
	if repo.get(epA).status != "failed" {
		t.Errorf("status = %q, want failed", repo.get(epA).status)
	}
	// Each attempt saw its context cancelled by the per-attempt timeout.
	if got := media.cancelled.Load(); got != 3 {
		t.Errorf("cancelled attempts = %d, want 3", got)
	}
}

func TestConcurrentClaimNoOp(t *testing.T) {
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	blob := newRemoteBlob(t)
	media := &fakeMedia{duration: 2 * time.Second}
	r := newRunner(repo, blob, media, Config{Retries: 2})

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = r.Run(context.Background(), epA, "ingest") }(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Run[%d] = %v, want nil (winner succeeds, losers no-op)", i, err)
		}
	}
	e := repo.get(epA)
	if e.claims != 1 {
		t.Errorf("claims = %d, want exactly 1 (compare-and-set)", e.claims)
	}
	if e.status != "ready" {
		t.Errorf("status = %q, want ready", e.status)
	}
	if got := media.renders.Load(); got != 1 {
		t.Errorf("render calls = %d, want 1 (stage ran once)", got)
	}
}

func TestCrossOrgIsolation(t *testing.T) {
	repo := newFakeRepo()
	masterA := orgA + "/" + epA + "/masters/m.mp4"
	repo.add(epA, orgA, masterA)
	repo.add(epB, orgB, orgB+"/"+epB+"/masters/m.mp4")
	blob := newRemoteBlob(t)
	media := &fakeMedia{duration: 2 * time.Second}
	r := newRunner(repo, blob, media, Config{Retries: 2})

	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("Run(epA): %v", err)
	}
	// Only org A's episode advanced; org B's identical-status episode is untouched.
	if a := repo.get(epA); a.status != "ready" {
		t.Errorf("epA status = %q, want ready", a.status)
	}
	if b := repo.get(epB); b.status != "uploaded" {
		t.Errorf("epB status = %q, want uploaded (untouched)", b.status)
	}
	// The proxy key is built under org A's prefix, never org B's.
	if a := repo.get(epA); !strings.Contains(a.proxyKey, orgA+"/"+epA) {
		t.Errorf("proxy key %q not scoped to org A", a.proxyKey)
	}
}

func TestUnknownStageErrors(t *testing.T) {
	r := newRunner(newFakeRepo(), newRemoteBlob(t), &fakeMedia{}, Config{})
	if err := r.Run(context.Background(), epA, "transcribe"); err == nil {
		t.Fatal("Run with unknown stage: want error, got nil")
	}
}

func TestMissingEpisodeNoOp(t *testing.T) {
	// An episode the repo does not know is a clean no-op (exit 0), not a fault.
	r := newRunner(newFakeRepo(), newRemoteBlob(t), &fakeMedia{}, Config{})
	if err := r.Run(context.Background(), "ep_missing", "ingest"); err != nil {
		t.Fatalf("Run on unknown episode = %v, want nil no-op", err)
	}
}

// TestRunShutdownMarksFailedBounded is the SIGTERM/shutdown path: the parent
// context is cancelled mid-stage (as signal.NotifyContext does when Cloud Run
// sends SIGTERM before SIGKILL). The run must still durably mark the claimed
// episode 'failed' — on a detached, bounded context, since the run's own context
// is now dead — and return promptly, well inside the ~10s grace window. The
// repo is set to honor context cancellation (markRespectsCtx), so a regression
// that finalized on the cancelled context would leave the episode 'processing'
// and fail this test.
func TestRunShutdownMarksFailedBounded(t *testing.T) {
	repo := newFakeRepo()
	repo.markRespectsCtx = true
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	blob := newRemoteBlob(t)
	media := &fakeMedia{blockOnCtx: true} // blocks until the context is cancelled
	// A long per-attempt timeout so only the parent cancel ends the attempt.
	r := newRunner(repo, blob, media, Config{StageTimeout: time.Minute, Retries: 2})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, epA, "ingest") }()

	// Let the stage reach the blocking render, then deliver the "SIGTERM".
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, ErrStageFailed) {
			t.Fatalf("Run err = %v, want ErrStageFailed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s of shutdown; grace-window bound violated")
	}
	if e := repo.get(epA); e.status != "failed" {
		t.Errorf("status = %q, want failed (episode must never be left in processing)", e.status)
	}
	if e := repo.get(epA); !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(e.errorID) {
		t.Errorf("error_id = %q, want a neutral 16-hex id", e.errorID)
	}
	// The stage was cancelled once (no retry after parent cancel).
	if got := media.cancelled.Load(); got != 1 {
		t.Errorf("cancelled attempts = %d, want 1 (no retry after shutdown)", got)
	}
}

// TestNotClaimableLogsWarnWithBlockingStatus asserts a refused claim logs at WARN
// (not INFO) and names the blocking status, so a retry attempt that observes an
// episode it cannot take is a visible signal rather than a silent success.
func TestNotClaimableLogsWarnWithBlockingStatus(t *testing.T) {
	repo := newFakeRepo()
	repo.add(epA, orgA, orgA+"/"+epA+"/masters/m.mp4")
	blob := newRemoteBlob(t)
	media := &fakeMedia{duration: 2 * time.Second}

	var buf syncBuffer
	r := &Runner{Repo: repo, Blob: blob, Media: media,
		Log:    slog.New(slog.NewJSONHandler(&buf, nil)),
		Config: Config{Retries: 2}}

	// First run claims and completes (episode -> ready).
	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	buf.reset()

	// Second run cannot claim (episode is 'ready'): expect a WARN naming the status.
	if err := r.Run(context.Background(), epA, "ingest"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	entry := map[string]any{}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatalf("unmarshal log line: %v (line=%q)", err, buf.String())
	}
	if entry["level"] != "WARN" {
		t.Errorf("log level = %v, want WARN", entry["level"])
	}
	if entry["msg"] != "episode not claimable; no-op" {
		t.Errorf("log msg = %v", entry["msg"])
	}
	if entry["blocking_status"] != "ready" {
		t.Errorf("blocking_status = %v, want ready", entry["blocking_status"])
	}
}

// syncBuffer is a concurrency-safe buffer for capturing a logger's output while
// the runner writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf.Bytes()...)
}

func (s *syncBuffer) String() string { return string(s.Bytes()) }

func (s *syncBuffer) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.Reset()
}
