package webembed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	h, err := Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return h
}

func do(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServesIndexAtRoot(t *testing.T) {
	h := newHandler(t)
	rec := do(t, h, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html*", ct)
	}
	if !strings.Contains(rec.Body.String(), "Blueshift Studio") {
		t.Errorf("body missing placeholder marker: %q", rec.Body.String())
	}
}

func TestServesNamedFile(t *testing.T) {
	h := newHandler(t)
	rec := do(t, h, http.MethodGet, "/index.html")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("expected html body, got %q", rec.Body.String())
	}
}

func TestSPAFallbackForUnknownPath(t *testing.T) {
	h := newHandler(t)
	rec := do(t, h, http.MethodGet, "/library/some/deep/route")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (SPA fallback)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Blueshift Studio") {
		t.Errorf("fallback did not serve index.html: %q", rec.Body.String())
	}
}

func TestAPIPathNotMaskedBySPA(t *testing.T) {
	h := newHandler(t)
	for _, p := range []string{"/api/", "/api/episodes"} {
		rec := do(t, h, http.MethodGet, p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", p, rec.Code)
		}
	}
}

func TestNoDirectoryListing(t *testing.T) {
	h := newHandler(t)
	// "/" resolves to the dist root directory; it must serve index.html, never
	// a directory listing.
	rec := do(t, h, http.MethodGet, "/")
	if strings.Contains(rec.Body.String(), "<pre>") || strings.Contains(rec.Body.String(), "Index of") {
		t.Errorf("directory listing leaked: %q", rec.Body.String())
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := newHandler(t)
	rec := do(t, h, http.MethodPost, "/")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / status = %d, want 405", rec.Code)
	}
}
