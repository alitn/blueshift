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
		"favicon.png":           {Data: []byte("png-bytes")},
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

// Caching policy per path class. The shell (root, explicit /index.html, and
// every SPA-fallback deep link) must revalidate on a normal refresh; hashed
// immutable assets cache forever; other static files get a modest max-age.
func TestCacheHeadersPerPathClass(t *testing.T) {
	h := newHandler(t)
	cases := []struct {
		name         string
		path         string
		wantCache    string
		wantShellTag bool
	}{
		{"shell root", "/", "no-cache", true},
		{"shell explicit", "/index.html", "no-cache", true},
		{"shell fallback deep link", "/episode/xyz", "no-cache", true},
		{"immutable asset", "/_app/immutable/app.js", "public, max-age=31536000, immutable", false},
		{"other static", "/favicon.png", "public, max-age=3600", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, h, http.MethodGet, c.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200", c.path, rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); got != c.wantCache {
				t.Errorf("GET %s Cache-Control = %q, want %q", c.path, got, c.wantCache)
			}
			etag := rec.Header().Get("ETag")
			if c.wantShellTag {
				// Strong ETag: quoted, no W/ prefix.
				if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) || len(etag) < 3 {
					t.Errorf("GET %s ETag = %q, want strong quoted ETag", c.path, etag)
				}
			} else if etag != "" {
				t.Errorf("GET %s ETag = %q, want none", c.path, etag)
			}
		})
	}
}

// An unchanged shell revalidates to 304 via If-None-Match, on the root and on
// SPA-fallback deep links alike.
func TestShellRevalidatesTo304(t *testing.T) {
	h := newHandler(t)
	first := do(t, h, http.MethodGet, "/")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("shell response carried no ETag")
	}

	for _, p := range []string{"/", "/episode/xyz"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Header.Set("If-None-Match", etag)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotModified {
			t.Errorf("GET %s with If-None-Match status = %d, want 304", p, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s 304 Cache-Control = %q, want %q", p, got, "no-cache")
		}
		if rec.Body.Len() != 0 {
			t.Errorf("GET %s 304 carried a body (%d bytes)", p, rec.Body.Len())
		}
	}

	// A different validator must still get the full body.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", `"stale-etag"`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET / with stale If-None-Match status = %d, want 200", rec.Code)
	}
}

// The shell ETag changes when the shell content changes, so a redeployed build
// never revalidates against the old validator.
func TestShellETagTracksContent(t *testing.T) {
	a := do(t, NewHandler(testFS()), http.MethodGet, "/")
	changed := testFS()
	changed["index.html"] = &fstest.MapFile{Data: []byte(indexHTML + "<!-- v2 -->")}
	b := do(t, NewHandler(changed), http.MethodGet, "/")
	if a.Header().Get("ETag") == b.Header().Get("ETag") {
		t.Errorf("ETag unchanged across shell content change: %q", a.Header().Get("ETag"))
	}
}
