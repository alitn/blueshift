package sweep

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRepo counts each sub-sweep independently and can return canned
// results/errors per sub-sweep. It is concurrency-safe because Run calls it from
// its own goroutine.
type fakeRepo struct {
	abandonedCalls atomic.Int64
	stuckCalls     atomic.Int64
	lastUploadTTL  atomic.Int64 // nanoseconds of the ttl seen on the last abandoned sweep
	lastStuckTTL   atomic.Int64 // nanoseconds of the ttl seen on the last stuck sweep

	mu           sync.Mutex
	abandonedN   int64
	stuckN       int64
	abandonedErr error
	stuckErr     error
}

func (f *fakeRepo) SweepAbandonedEpisodes(_ context.Context, ttl time.Duration) (int64, error) {
	f.abandonedCalls.Add(1)
	f.lastUploadTTL.Store(int64(ttl))
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.abandonedN, f.abandonedErr
}

func (f *fakeRepo) SweepStuckProcessingEpisodes(_ context.Context, ttl time.Duration) (int64, error) {
	f.stuckCalls.Add(1)
	f.lastStuckTTL.Store(int64(ttl))
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stuckN, f.stuckErr
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// TestRunTicksAndStops drives the loop with a short interval and asserts it runs
// BOTH sub-sweeps repeatedly, passes each configured TTL through to the right
// sub-sweep, and returns promptly once the context is cancelled. No sleep here
// exceeds a fraction of a second.
func TestRunTicksAndStops(t *testing.T) {
	repo := &fakeRepo{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		Run(ctx, repo, discardLogger(), 15*time.Millisecond, 6*time.Hour, 5*time.Hour)
		close(done)
	}()

	// Give the ticker room for several iterations, then stop it.
	time.Sleep(120 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of ctx cancel")
	}

	if got := repo.abandonedCalls.Load(); got < 2 {
		t.Errorf("abandoned sweep calls = %d, want >= 2 (ticker did not fire repeatedly)", got)
	}
	if got := repo.stuckCalls.Load(); got < 2 {
		t.Errorf("stuck-processing sweep calls = %d, want >= 2 (ticker did not fire repeatedly)", got)
	}
	if got := time.Duration(repo.lastUploadTTL.Load()); got != 6*time.Hour {
		t.Errorf("upload ttl passed to repo = %v, want 6h", got)
	}
	if got := time.Duration(repo.lastStuckTTL.Load()); got != 5*time.Hour {
		t.Errorf("processing ttl passed to repo = %v, want 5h", got)
	}
}

// TestRunFirstDelayCapsAtInterval verifies the boot grace period never delays
// the first sweep past one interval: with a sub-DefaultFirstDelay interval, the
// first sweep must land quickly rather than after a full minute.
func TestRunFirstDelayCapsAtInterval(t *testing.T) {
	repo := &fakeRepo{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go Run(ctx, repo, discardLogger(), 10*time.Millisecond, time.Hour, time.Hour)

	deadline := time.After(500 * time.Millisecond)
	for repo.abandonedCalls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("first sweep did not fire within 500ms; first delay not capped at interval")
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// TestRunSurvivesSweepErrors asserts a failure in EITHER sub-sweep is logged but
// does not kill the loop, and never skips the other sub-sweep: with both
// sub-sweeps erroring, both keep firing and both errors are logged.
func TestRunSurvivesSweepErrors(t *testing.T) {
	repo := &fakeRepo{abandonedErr: errors.New("db blip"), stuckErr: errors.New("db blip 2")}
	var buf syncBuffer
	ctx, cancel := context.WithCancel(context.Background())

	go Run(ctx, repo, slog.New(slog.NewJSONHandler(&buf, nil)), 10*time.Millisecond, time.Hour, time.Hour)
	time.Sleep(80 * time.Millisecond)
	cancel()

	if got := repo.abandonedCalls.Load(); got < 2 {
		t.Errorf("abandoned sweep calls = %d, want >= 2 (loop died on error)", got)
	}
	if got := repo.stuckCalls.Load(); got < 2 {
		t.Errorf("stuck sweep calls = %d, want >= 2 (an errored sub-sweep must not skip the other)", got)
	}
	out := buf.Bytes()
	if !bytes.Contains(out, []byte("abandoned-upload sweep failed")) {
		t.Error("abandoned sweep error was not logged")
	}
	if !bytes.Contains(out, []byte("stuck-processing sweep failed")) {
		t.Error("stuck-processing sweep error was not logged")
	}
}

// TestSweepOnceRunsBothSubSweeps asserts a single tick runs each sub-sweep
// exactly once, passing the correct TTL to each.
func TestSweepOnceRunsBothSubSweeps(t *testing.T) {
	repo := &fakeRepo{}
	sweepOnce(context.Background(), repo, discardLogger(), 6*time.Hour, 5*time.Hour)
	if got := repo.abandonedCalls.Load(); got != 1 {
		t.Errorf("abandoned sweep calls = %d, want 1", got)
	}
	if got := repo.stuckCalls.Load(); got != 1 {
		t.Errorf("stuck sweep calls = %d, want 1", got)
	}
	if got := time.Duration(repo.lastUploadTTL.Load()); got != 6*time.Hour {
		t.Errorf("upload ttl = %v, want 6h", got)
	}
	if got := time.Duration(repo.lastStuckTTL.Load()); got != 5*time.Hour {
		t.Errorf("processing ttl = %v, want 5h", got)
	}
}

// TestSweepAbandonedUploadsLogging asserts a non-zero abandoned sweep logs an
// INFO line with the count, and a zero sweep stays silent (no hourly noise).
func TestSweepAbandonedUploadsLogging(t *testing.T) {
	var buf bytes.Buffer
	sweepAbandonedUploads(context.Background(), &fakeRepo{abandonedN: 3}, slog.New(slog.NewJSONHandler(&buf, nil)), 6*time.Hour)
	entry := decodeLine(t, buf.Bytes())
	if entry["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", entry["level"])
	}
	if entry["count"].(float64) != 3 {
		t.Errorf("count = %v, want 3", entry["count"])
	}

	var quiet bytes.Buffer
	sweepAbandonedUploads(context.Background(), &fakeRepo{abandonedN: 0}, slog.New(slog.NewJSONHandler(&quiet, nil)), 6*time.Hour)
	if quiet.Len() != 0 {
		t.Errorf("zero-count abandoned sweep logged %q, want silence", quiet.String())
	}
}

// TestSweepStuckProcessingLogging asserts a non-zero stuck-processing sweep logs
// at WARN (a killed worker is a signal, not routine) with the count, and a zero
// sweep stays silent.
func TestSweepStuckProcessingLogging(t *testing.T) {
	var buf bytes.Buffer
	sweepStuckProcessing(context.Background(), &fakeRepo{stuckN: 2}, slog.New(slog.NewJSONHandler(&buf, nil)), 5*time.Hour)
	entry := decodeLine(t, buf.Bytes())
	if entry["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", entry["level"])
	}
	if entry["msg"] != "swept stuck processing episodes" {
		t.Errorf("msg = %v", entry["msg"])
	}
	if entry["count"].(float64) != 2 {
		t.Errorf("count = %v, want 2", entry["count"])
	}

	var quiet bytes.Buffer
	sweepStuckProcessing(context.Background(), &fakeRepo{stuckN: 0}, slog.New(slog.NewJSONHandler(&quiet, nil)), 5*time.Hour)
	if quiet.Len() != 0 {
		t.Errorf("zero-count stuck sweep logged %q, want silence", quiet.String())
	}
}

func decodeLine(t *testing.T, b []byte) map[string]any {
	t.Helper()
	entry := map[string]any{}
	if err := json.Unmarshal(bytes.TrimSpace(b), &entry); err != nil {
		t.Fatalf("unmarshal log line: %v (line=%q)", err, string(b))
	}
	return entry
}

// syncBuffer is a tiny concurrency-safe bytes.Buffer wrapper for the logger the
// sweep goroutine writes to while the test reads it.
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
