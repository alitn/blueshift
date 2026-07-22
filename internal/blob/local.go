package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LocalBasePath is the URL subtree the local store serves its PUT/GET endpoint
// under. It is mounted ahead of the authenticated /api gate: local upload URLs
// carry a short-lived HMAC token instead of the session cookie, so a plain
// `fetch`/`curl` of the signed URL works exactly as a GCS signed URL would.
const LocalBasePath = "/api/blob/local"

// localObjectPath is the single endpoint under LocalBasePath; the object key and
// the authorized operation both live inside the signed token, never the URL.
const localObjectPath = LocalBasePath + "/object"

// localUploadTTL / localGetTTL bound how long a minted local URL stays valid.
const (
	localUploadTTL = 6 * time.Hour
	localGetTTL    = 1 * time.Hour
)

// Local is the filesystem-backed Store used by `make demo` and tests. Objects
// live under dir at their key path; "signing" is an HMAC token verified by the
// handler this type serves. It is not for production use — gcs.go is.
type Local struct {
	dir    string
	signer *signer
}

var _ Store = (*Local)(nil)

// NewLocal returns a Local rooted at dir (created if missing), signing tokens
// with key (the session secret) and reading time from now (nil = time.Now).
func NewLocal(dir string, key []byte, now func() time.Time) (*Local, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("blob: local dir is empty")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("blob: create local dir: %w", err)
	}
	return &Local{dir: dir, signer: newSigner(key, now)}, nil
}

// InitResumableUpload mints a PUT URL for key. The returned URL is same-origin
// relative so the browser (or curl against the app host) uploads directly.
func (l *Local) InitResumableUpload(_ context.Context, key, contentType string, _ int64) (Upload, error) {
	tok, err := l.signer.mint(key, opPut, localUploadTTL)
	if err != nil {
		return Upload{}, err
	}
	headers := map[string]string{}
	if contentType != "" {
		headers["Content-Type"] = contentType
	}
	return Upload{URL: localObjectPath + "?t=" + url.QueryEscape(tok), Method: http.MethodPut, Headers: headers}, nil
}

// Stat returns the on-disk size of key, or ErrNotFound.
func (l *Local) Stat(_ context.Context, key string) (int64, error) {
	p, err := l.resolve(key)
	if err != nil {
		return 0, err
	}
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("blob: stat: %w", err)
	}
	return fi.Size(), nil
}

// SignedGetURL mints a short-lived GET URL for key. It does not require the
// object to exist yet (the token is a grant, not a promise); a later GET of a
// missing object returns 404.
func (l *Local) SignedGetURL(_ context.Context, key string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = localGetTTL
	}
	tok, err := l.signer.mint(key, opGet, ttl)
	if err != nil {
		return "", err
	}
	return localObjectPath + "?t=" + url.QueryEscape(tok), nil
}

// LocalPath returns the absolute on-disk path backing key, confined to the
// store root. It lets pipeline tooling (ffmpeg) read a master and write its
// renders in place without a copy, which is why the local store is preferred
// over Download/Upload when the pipeline detects it. The object need not exist
// yet: callers writing an output MkdirAll its parent first.
func (l *Local) LocalPath(key string) (string, error) {
	p, err := l.resolve(key)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("blob: abs path: %w", err)
	}
	return abs, nil
}

// Download copies the object at key to destPath (parent created). It exists so
// the local store satisfies the same movement contract as GCS; the pipeline
// normally uses LocalPath instead to avoid the copy.
func (l *Local) Download(_ context.Context, key, destPath string) error {
	src, err := l.resolve(key)
	if err != nil {
		return err
	}
	in, err := os.Open(src) //nolint:gosec // src is confined to l.dir by resolve.
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("blob: open source: %w", err)
	}
	defer func() { _ = in.Close() }()
	return copyToFile(in, destPath)
}

// Upload copies the local file at srcPath to the object at key (parent created).
// contentType is ignored by the filesystem store.
func (l *Local) Upload(_ context.Context, key, srcPath, _ string) error {
	dst, err := l.resolve(key)
	if err != nil {
		return err
	}
	in, err := os.Open(srcPath) //nolint:gosec // srcPath is a pipeline-produced temp file.
	if err != nil {
		return fmt.Errorf("blob: open output: %w", err)
	}
	defer func() { _ = in.Close() }()
	return copyToFile(in, dst)
}

// copyToFile streams r into destPath, creating the parent directory and writing
// via a temp sibling + rename so a reader never sees a partial object.
func copyToFile(r io.Reader, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o750); err != nil {
		return fmt.Errorf("blob: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".copy-*")
	if err != nil {
		return fmt.Errorf("blob: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("blob: copy: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("blob: close: %w", err)
	}
	if err := os.Rename(tmpName, destPath); err != nil {
		return fmt.Errorf("blob: rename: %w", err)
	}
	return nil
}

// Handler serves the local PUT/GET endpoint. Mount it at LocalBasePath+"/",
// ahead of the /api auth gate. It authenticates by verifying the token in ?t=;
// there is no session cookie on this path.
func (l *Local) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT "+localObjectPath, l.put)
	mux.HandleFunc("GET "+localObjectPath, l.get)
	return mux
}

func (l *Local) put(w http.ResponseWriter, r *http.Request) {
	key, ok := l.authorize(w, r, opPut)
	if !ok {
		return
	}
	p, err := l.resolve(key)
	if err != nil {
		http.Error(w, "bad key", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	// Write to a temp sibling then rename so a reader/stat never sees a partial
	// object, mirroring the atomic finalize of a real resumable upload.
	tmp, err := os.CreateTemp(filepath.Dir(p), ".upload-*")
	if err != nil {
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := io.Copy(tmp, r.Body); err != nil {
		_ = tmp.Close()
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	if err := tmp.Close(); err != nil {
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmpName, p); err != nil {
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (l *Local) get(w http.ResponseWriter, r *http.Request) {
	key, ok := l.authorize(w, r, opGet)
	if !ok {
		return
	}
	p, err := l.resolve(key)
	if err != nil {
		http.Error(w, "bad key", http.StatusBadRequest)
		return
	}
	f, err := os.Open(p) //nolint:gosec // p is confined to l.dir by resolve.
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "read failed", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, filepath.Base(p), time.Time{}, f)
}

// authorize verifies the ?t= token for the given op and returns the authorized
// key. On any failure it writes a neutral status and returns ok=false.
func (l *Local) authorize(w http.ResponseWriter, r *http.Request, o op) (string, bool) {
	tok := r.URL.Query().Get("t")
	if tok == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return "", false
	}
	key, err := l.signer.verify(tok, o)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return "", false
	}
	return key, true
}

// resolve maps a storage key to an on-disk path confined to l.dir, rejecting any
// key that would escape the root (defense in depth; keys are already built from
// sanitized components).
func (l *Local) resolve(key string) (string, error) {
	if key == "" {
		return "", ErrBadFilename
	}
	clean := filepath.Clean(filepath.FromSlash(key))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrBadFilename
	}
	p := filepath.Join(l.dir, clean)
	root := filepath.Clean(l.dir) + string(filepath.Separator)
	if p != filepath.Clean(l.dir) && !strings.HasPrefix(p, root) {
		return "", ErrBadFilename
	}
	return p, nil
}
