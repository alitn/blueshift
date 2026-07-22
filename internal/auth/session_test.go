package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestSessionRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := NewCodec("key-a")
	c.now = fixedClock(now)

	want := Session{
		Email:       "dev-approver@blueshift.local",
		OrgPublicID: "0192f0aa-1111-7abc-8def-000000000001",
		Role:        "approver",
		ExpiresAt:   now.Add(SessionTTL),
	}
	tok, err := c.Mint(want)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	got, err := c.Verify(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Email != want.Email || got.OrgPublicID != want.OrgPublicID || got.Role != want.Role {
		t.Errorf("round trip mismatch: got %+v want %+v", got, want)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt.Truncate(time.Second)) {
		t.Errorf("expiry = %v, want %v", got.ExpiresAt, want.ExpiresAt)
	}
}

func TestSessionExpired(t *testing.T) {
	issued := time.Unix(1_700_000_000, 0)
	c := NewCodec("key-a")
	c.now = fixedClock(issued)
	tok, err := c.Mint(Session{Email: "e", ExpiresAt: issued.Add(SessionTTL)})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	// Advance past expiry.
	c.now = fixedClock(issued.Add(SessionTTL + time.Second))
	if _, err := c.Verify(tok); !errors.Is(err, ErrExpired) {
		t.Fatalf("verify expired = %v, want ErrExpired", err)
	}
}

func TestSessionTamperDetection(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := NewCodec("key-a")
	c.now = fixedClock(now)
	tok, err := c.Mint(Session{Email: "e", Role: "editor", ExpiresAt: now.Add(SessionTTL)})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	dot := strings.IndexByte(tok, '.')
	flip := func(s string) string {
		b := []byte(s)
		if b[0] == 'A' {
			b[0] = 'B'
		} else {
			b[0] = 'A'
		}
		return string(b)
	}
	tampered := flip(tok[:dot]) + tok[dot:] // mutate payload, keep signature

	if _, err := c.Verify(tampered); !errors.Is(err, ErrTampered) {
		t.Fatalf("verify tampered payload = %v, want ErrTampered", err)
	}
	for _, bad := range []string{"", "noseparator", tok[:dot], "." + tok, tok + "."} {
		if _, err := c.Verify(bad); !errors.Is(err, ErrTampered) {
			t.Errorf("verify(%q) = %v, want ErrTampered", bad, err)
		}
	}
}

func TestSessionWrongKey(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	signer := NewCodec("key-a")
	signer.now = fixedClock(now)
	tok, err := signer.Mint(Session{Email: "e", ExpiresAt: now.Add(SessionTTL)})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	other := NewCodec("key-b")
	other.now = fixedClock(now)
	if _, err := other.Verify(tok); !errors.Is(err, ErrTampered) {
		t.Fatalf("verify with wrong key = %v, want ErrTampered", err)
	}
}
