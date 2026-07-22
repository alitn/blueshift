package api

import (
	"testing"
	"time"
)

func TestRateLimiterBurstThenRefill(t *testing.T) {
	now := time.Unix(0, 0)
	clock := func() time.Time { return now }
	l := newRateLimiter(5, time.Minute, clock)

	// 5 allowed in the burst, 6th denied.
	for i := 0; i < 5; i++ {
		if !l.allow("1.2.3.4") {
			t.Fatalf("request %d denied, want allowed", i+1)
		}
	}
	if l.allow("1.2.3.4") {
		t.Fatal("6th request allowed, want denied")
	}

	// A different key has its own bucket.
	if !l.allow("5.6.7.8") {
		t.Fatal("distinct key denied")
	}

	// After 12s one token has refilled (5/min).
	now = now.Add(12 * time.Second)
	if !l.allow("1.2.3.4") {
		t.Fatal("request after refill denied, want allowed")
	}
	if l.allow("1.2.3.4") {
		t.Fatal("second request after single refill allowed, want denied")
	}
}

func TestRateLimiterRefillCaps(t *testing.T) {
	now := time.Unix(0, 0)
	l := newRateLimiter(5, time.Minute, func() time.Time { return now })
	if !l.allow("k") {
		t.Fatal("first denied")
	}
	// A long idle period must not let tokens exceed capacity.
	now = now.Add(time.Hour)
	for i := 0; i < 5; i++ {
		if !l.allow("k") {
			t.Fatalf("post-idle request %d denied", i+1)
		}
	}
	if l.allow("k") {
		t.Fatal("tokens exceeded capacity after idle")
	}
}
