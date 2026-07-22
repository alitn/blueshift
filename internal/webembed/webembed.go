// Package webembed serves the SvelteKit SPA build. In production the build is
// embedded from dist/ (populated by `make build`: web build → copied here → go
// build). The serving logic takes an fs.FS so it can be exercised in tests with
// an in-memory filesystem (fstest.MapFS) rather than the embedded bytes.
//
// Serving is SPA-style: known files are served by path, and any other non-API
// path falls back to index.html so client-side routing works.
package webembed

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// distFS embeds the built SPA. The committed tree carries only a .gitkeep (the
// build output is gitignored); `all:dist` still embeds successfully from that,
// and `make build` fills dist/ with the real build before `go build`.
//
//go:embed all:dist
var distFS embed.FS

const indexFile = "index.html"

// Handler returns the production handler backed by the embedded build.
func Handler() (http.Handler, error) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, err
	}
	return NewHandler(sub), nil
}

// NewHandler returns an http.Handler that serves the SPA from fsys. It never
// lists directories, and it never falls back to index.html for /api/ paths so
// unmatched API requests surface as 404s rather than HTML. Callers (and tests)
// supply any fs.FS whose root holds index.html and the static assets.
func NewHandler(fsys fs.FS) http.Handler {
	return &spaHandler{fsys: fsys}
}

type spaHandler struct {
	fsys fs.FS
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clean := path.Clean("/" + r.URL.Path)

	// API paths are owned by the API layer; never mask a miss with the SPA.
	if clean == "/api" || strings.HasPrefix(clean, "/api/") {
		http.NotFound(w, r)
		return
	}

	name := strings.TrimPrefix(clean, "/")
	if name == "" {
		name = indexFile
	}

	if h.serveFile(w, r, name) {
		return
	}
	// SPA fallback: unknown path -> index.html for client-side routing.
	if h.serveFile(w, r, indexFile) {
		return
	}
	http.NotFound(w, r)
}

// serveFile writes the named file if it exists as a regular file. It returns
// false (serving nothing) for missing paths and directories, so callers can
// fall back without any directory listing ever being produced.
func (h *spaHandler) serveFile(w http.ResponseWriter, r *http.Request, name string) bool {
	f, err := h.fsys.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}

	rs, ok := f.(io.ReadSeeker)
	if !ok {
		data, err := io.ReadAll(f)
		if err != nil {
			return false
		}
		rs = bytes.NewReader(data)
	}

	http.ServeContent(w, r, info.Name(), info.ModTime(), rs)
	return true
}
