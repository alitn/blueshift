package api

import (
	"sync"
	"time"
)

// rateLimiter is an in-memory per-key token bucket (stdlib only). Each key
// (client IP) gets a bucket that refills continuously to `capacity` tokens over
// `window`; one request costs one token. It is a light abuse control on the
// login endpoint, not a distributed rate limit.
type rateLimiter struct {
	mu           sync.Mutex
	buckets      map[string]*tokenBucket
	capacity     float64
	refillPerSec float64
	window       time.Duration
	now          func() time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// sweepThreshold bounds the bucket map: once it grows past this, idle full
// buckets are dropped on the next allow() so memory stays bounded under churn.
const sweepThreshold = 4096

// newRateLimiter allows up to perWindow requests per key per window.
func newRateLimiter(perWindow float64, window time.Duration, now func() time.Time) *rateLimiter {
	if now == nil {
		now = time.Now
	}
	return &rateLimiter{
		buckets:      make(map[string]*tokenBucket),
		capacity:     perWindow,
		refillPerSec: perWindow / window.Seconds(),
		window:       window,
		now:          now,
	}
}

// allow reports whether a request from key may proceed, consuming a token.
func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	t := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = &tokenBucket{tokens: l.capacity, last: t}
		l.buckets[key] = b
	} else if elapsed := t.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = minFloat(l.capacity, b.tokens+elapsed*l.refillPerSec)
		b.last = t
	}

	if len(l.buckets) > sweepThreshold {
		l.sweep(t)
	}

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// sweep drops buckets that are full (never rate-limited) and idle beyond the
// window — they carry no state worth keeping.
func (l *rateLimiter) sweep(t time.Time) {
	for k, b := range l.buckets {
		if b.tokens >= l.capacity && t.Sub(b.last) > l.window {
			delete(l.buckets, k)
		}
	}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
