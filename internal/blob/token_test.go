package blob

import (
	"errors"
	"testing"
	"time"
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestTokenRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newSigner([]byte("secret"), fixedClock(now))
	tok, err := s.mint("org_a/ep_b/masters/x.mp4", opPut, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	key, err := s.verify(tok, opPut)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if key != "org_a/ep_b/masters/x.mp4" {
		t.Fatalf("key = %q", key)
	}
}

func TestTokenExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	minted := newSigner([]byte("secret"), fixedClock(now))
	tok, err := minted.mint("k", opGet, time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	// A verifier whose clock is past the expiry rejects it.
	later := newSigner([]byte("secret"), fixedClock(now.Add(2*time.Minute)))
	if _, err := later.verify(tok, opGet); !errors.Is(err, ErrExpired) {
		t.Fatalf("verify expired err = %v, want ErrExpired", err)
	}
}

func TestTokenTamper(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newSigner([]byte("secret"), fixedClock(now))
	tok, err := s.mint("k", opPut, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	// Flip a byte in the payload.
	bad := []byte(tok)
	bad[0] ^= 0x01
	if _, err := s.verify(string(bad), opPut); !errors.Is(err, ErrTampered) {
		t.Fatalf("verify tampered err = %v, want ErrTampered", err)
	}
	// Wrong key.
	other := newSigner([]byte("different"), fixedClock(now))
	if _, err := other.verify(tok, opPut); !errors.Is(err, ErrTampered) {
		t.Fatalf("verify wrong-key err = %v, want ErrTampered", err)
	}
	// Structurally broken.
	for _, s2 := range []string{"", ".", "abc", tok[:len(tok)/2]} {
		if _, err := s.verify(s2, opPut); !errors.Is(err, ErrTampered) {
			t.Errorf("verify(%q) err = %v, want ErrTampered", s2, err)
		}
	}
}

func TestTokenOperationBinding(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newSigner([]byte("secret"), fixedClock(now))
	tok, err := s.mint("k", opGet, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	// A read grant must not authorize a write.
	if _, err := s.verify(tok, opPut); !errors.Is(err, ErrTampered) {
		t.Fatalf("get token used for put err = %v, want ErrTampered", err)
	}
}
