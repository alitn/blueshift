// Package webembed serves the embedded web build. The committed dist/ here is a
// placeholder; the real SvelteKit build replaces its contents via the Makefile
// in a later task. Serving is SPA-style: known files are served by path, and
// any other non-API path falls back to index.html so client-side routing works.
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

//go:embed all:dist
var distFS embed.FS

const indexFile = "index.html"

// Handler returns an http.Handler that serves the embedded build. It never
// lists directories, and it never falls back to index.html for /api/ paths so
// unmatched API requests surface as 404s rather than HTML.
func Handler() (http.Handler, error) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, err
	}
	return &spaHandler{fsys: sub}, nil
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
