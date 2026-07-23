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

// fakeRepo counts sweep calls and can return a canned result/error. It is
// concurrency-safe because Run calls it from its own goroutine.
type fakeRepo struct {
	calls   atomic.Int64
	lastTTL atomic.Int64 // nanoseconds of the ttl seen on the last call
	mu      sync.Mutex
	n       int64
	err     error
}

func (f *fakeRepo) SweepAbandonedEpisodes(_ context.Context, ttl time.Duration) (int64, error) {
	f.calls.Add(1)
	f.lastTTL.Store(int64(ttl))
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n, f.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// TestRunTicksAndStops drives the loop with a short interval and asserts it
// sweeps repeatedly, passes the configured TTL through, and returns promptly
// once the context is cancelled. No sleep here exceeds a fraction of a second.
func TestRunTicksAndStops(t *testing.T) {
	repo := &fakeRepo{n: 0}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		Run(ctx, repo, discardLogger(), 15*time.Millisecond, 6*time.Hour)
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

	if got := repo.calls.Load(); got < 2 {
		t.Errorf("sweep calls = %d, want >= 2 (ticker did not fire repeatedly)", got)
	}
	if got := time.Duration(repo.lastTTL.Load()); got != 6*time.Hour {
		t.Errorf("ttl passed to repo = %v, want 6h", got)
	}
}

// TestRunFirstDelayCapsAtInterval verifies the boot grace period never delays
// the first sweep past one interval: with a sub-DefaultFirstDelay interval, the
// first sweep must land quickly rather than after a full minute.
func TestRunFirstDelayCapsAtInterval(t *testing.T) {
	repo := &fakeRepo{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go Run(ctx, repo, discardLogger(), 10*time.Millisecond, time.Hour)

	deadline := time.After(500 * time.Millisecond)
	for repo.calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("first sweep did not fire within 500ms; first delay not capped at interval")
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// TestRunSurvivesSweepError asserts a failing sweep is logged but does not kill
// the loop: subsequent ticks keep firing.
func TestRunSurvivesSweepError(t *testing.T) {
	repo := &fakeRepo{err: errors.New("db blip")}
	var buf syncBuffer
	ctx, cancel := context.WithCancel(context.Background())

	go Run(ctx, repo, slog.New(slog.NewJSONHandler(&buf, nil)), 10*time.Millisecond, time.Hour)
	time.Sleep(80 * time.Millisecond)
	cancel()

	if got := repo.calls.Load(); got < 2 {
		t.Errorf("sweep calls = %d, want >= 2 (loop died on error)", got)
	}
	// The failure must be logged (server-side) at least once.
	if !bytes.Contains(buf.Bytes(), []byte("abandoned-upload sweep failed")) {
		t.Error("sweep error was not logged")
	}
}

// TestSweepOnceLogsCountWhenNonZero asserts a non-zero sweep logs an INFO line
// with the count, and a zero sweep stays silent (no noise every hour).
func TestSweepOnceLogsCountWhenNonZero(t *testing.T) {
	// Non-zero: one INFO line with count.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	sweepOnce(context.Background(), &fakeRepo{n: 3}, logger, 6*time.Hour)
	entry := map[string]any{}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatalf("unmarshal log line: %v (line=%q)", err, buf.String())
	}
	if entry["count"].(float64) != 3 {
		t.Errorf("logged count = %v, want 3", entry["count"])
	}

	// Zero: no output.
	var quiet bytes.Buffer
	sweepOnce(context.Background(), &fakeRepo{n: 0}, slog.New(slog.NewJSONHandler(&quiet, nil)), 6*time.Hour)
	if quiet.Len() != 0 {
		t.Errorf("zero-count sweep logged %q, want silence", quiet.String())
	}
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
