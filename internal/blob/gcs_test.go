package blob

import "testing"

// TestGCSEmptyBucket is the one thing about the GCS impl unit-testable without
// network: construction rejects an empty bucket. The signing/stat paths are
// exercised in staging. The compile-time `var _ Store = (*GCS)(nil)` in gcs.go
// guarantees the impl stays in sync with the interface.
func TestGCSEmptyBucket(t *testing.T) {
	if _, err := NewGCS(t.Context(), ""); err == nil {
		t.Fatal("NewGCS(\"\") = nil err, want rejection")
	}
}
