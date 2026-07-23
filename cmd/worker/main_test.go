package main

import (
	"context"
	"syscall"
	"testing"
	"time"
)

// TestShutdownContextCancelsOnSIGTERM proves the worker traps SIGTERM — the stop
// signal Cloud Run sends a task ~10s before SIGKILL — and cancels the run
// context, which is what lets the pipeline tear down ffmpeg and mark the claimed
// episode failed within the grace window. If SIGTERM were not in the trapped set
// its default action would terminate this test binary, so a green run also proves
// the signal is handled, not fatal. Bounded well under a second; no long sleeps.
func TestShutdownContextCancelsOnSIGTERM(t *testing.T) {
	ctx, cancel := shutdownContext()
	defer cancel()

	if ctx.Err() != nil {
		t.Fatalf("context already cancelled before signal: %v", ctx.Err())
	}
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("raise SIGTERM: %v", err)
	}

	select {
	case <-ctx.Done():
		if got := ctx.Err(); got != context.Canceled {
			t.Errorf("ctx.Err() = %v, want context.Canceled", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SIGTERM did not cancel the shutdown context within 2s")
	}
}
