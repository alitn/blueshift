package blob

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newLocal(t *testing.T) (*Local, string) {
	t.Helper()
	dir := t.TempDir()
	l, err := NewLocal(dir, []byte("test-secret"), func() time.Time { return time.Unix(1_700_000_000, 0) })
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return l, dir
}

// TestLocalRoundTrip drives the full local flow through the served handler:
// init upload -> PUT bytes to the signed URL -> Stat -> signed GET returns the
// same bytes.
func TestLocalRoundTrip(t *testing.T) {
	l, dir := newLocal(t)
	srv := httptest.NewServer(l.Handler())
	defer srv.Close()

	ctx := context.Background()
	key := "org_a/ep_b/masters/clip.mp4"
	body := []byte("the master bytes")

	up, err := l.InitResumableUpload(ctx, key, "video/mp4", "", int64(len(body)))
	if err != nil {
		t.Fatalf("InitResumableUpload: %v", err)
	}
	if up.Method != http.MethodPut {
		t.Fatalf("method = %q, want PUT", up.Method)
	}
	if !strings.HasPrefix(up.URL, LocalBasePath) {
		t.Fatalf("url = %q, want under %q", up.URL, LocalBasePath)
	}

	// PUT the bytes to the signed URL against the live handler.
	req, _ := http.NewRequest(http.MethodPut, srv.URL+up.URL, strings.NewReader(string(body)))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}

	// Bytes landed at the expected on-disk path.
	onDisk := filepath.Join(dir, filepath.FromSlash(key))
	got, err := os.ReadFile(onDisk) //nolint:gosec // test path.
	if err != nil {
		t.Fatalf("read on-disk: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("on-disk bytes = %q, want %q", got, body)
	}

	// Stat matches.
	size, err := l.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if size != int64(len(body)) {
		t.Fatalf("Stat size = %d, want %d", size, len(body))
	}

	// Signed GET returns the bytes.
	getURL, err := l.SignedGetURL(ctx, key, time.Hour)
	if err != nil {
		t.Fatalf("SignedGetURL: %v", err)
	}
	gresp, err := srv.Client().Get(srv.URL + getURL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = gresp.Body.Close() }()
	if gresp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", gresp.StatusCode)
	}
	gbytes, _ := io.ReadAll(gresp.Body)
	if string(gbytes) != string(body) {
		t.Fatalf("GET bytes = %q, want %q", gbytes, body)
	}
}

func TestLocalStatMissing(t *testing.T) {
	l, _ := newLocal(t)
	if _, err := l.Stat(context.Background(), "org_a/ep_b/masters/nope.mp4"); err != ErrNotFound {
		t.Fatalf("Stat missing err = %v, want ErrNotFound", err)
	}
}

func TestLocalPutRejectsMissingToken(t *testing.T) {
	l, _ := newLocal(t)
	srv := httptest.NewServer(l.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut, srv.URL+localObjectPath, strings.NewReader("x"))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestLocalGetMissingObject404(t *testing.T) {
	l, _ := newLocal(t)
	srv := httptest.NewServer(l.Handler())
	defer srv.Close()

	getURL, err := l.SignedGetURL(context.Background(), "org_a/ep_b/masters/ghost.mp4", time.Hour)
	if err != nil {
		t.Fatalf("SignedGetURL: %v", err)
	}
	resp, err := srv.Client().Get(srv.URL + getURL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestLocalResolveConfinement(t *testing.T) {
	l, dir := newLocal(t)
	for _, key := range []string{"../escape", "../../etc/passwd", "/abs/path"} {
		p, err := l.resolve(key)
		if err != nil {
			continue // rejected outright: fine.
		}
		if !strings.HasPrefix(p, filepath.Clean(dir)) {
			t.Errorf("resolve(%q) = %q escaped %q", key, p, dir)
		}
	}
}
