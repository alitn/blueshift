package webembed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// indexHTML is a stand-in for the built SvelteKit index; it carries a stable
// marker the SPA-fallback assertions look for.
const indexHTML = `<!doctype html><html><head><title>Blueshift Studio</title></head>` +
	`<body><div id="app">Blueshift Studio</div></body></html>`

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":            {Data: []byte(indexHTML)},
		"_app/immutable/app.js": {Data: []byte("console.log('app')")},
	}
}

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	return NewHandler(testFS())
}

func do(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Handler wires the embedded FS; it must build without error even when dist/
// holds only the committed .gitkeep placeholder.
func TestProdHandlerBuilds(t *testing.T) {
	if _, err := Handler(); err != nil {
		t.Fatalf("Handler: %v", err)
	}
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
		t.Errorf("body missing index marker: %q", rec.Body.String())
	}
}

func TestServesNamedFile(t *testing.T) {
	h := newHandler(t)
	rec := do(t, h, http.MethodGet, "/_app/immutable/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "console.log") {
		t.Errorf("expected asset body, got %q", rec.Body.String())
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
	// "/" resolves to the root directory; it must serve index.html, never a
	// directory listing.
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
