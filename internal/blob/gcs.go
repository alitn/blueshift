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

	"cloud.google.com/go/storage"
)

// resumableStartHeader is the header that turns a signed POST into a resumable
// upload session initiation; the response Location is the session URI the client
// then PUTs the bytes to.
const resumableStartHeader = "x-goog-resumable:start"

// uploadTTL / getTTL bound how long a signed URL stays valid.
const (
	uploadTTL = 6 * time.Hour
	getTTL    = 1 * time.Hour
)

// initSessionTimeout bounds the server-side resumable-session initiation POST.
// It is a small metadata round-trip, not a byte transfer, so a short timeout is
// safe and keeps a stuck provider from blocking the create request.
const initSessionTimeout = 30 * time.Second

// maxInitErrorBody caps how much of a provider error body we drain (never
// surfaced; drained only so the connection can be reused).
const maxInitErrorBody = 1 << 16

// GCS is the production Store. Signing uses the credentials the client is
// constructed with; on Cloud Run that is the service account resolved from the
// metadata server, whose SignBlob capability the storage client uses to produce
// V4 signatures — no private key file ever lives in the repo or image.
//
// Network behaviour is exercised in staging, not in unit tests; the unit build
// only compiles this file and asserts it satisfies Store.
type GCS struct {
	client     *storage.Client
	bucket     string
	httpClient *http.Client
}

var _ Store = (*GCS)(nil)

// NewGCS opens a storage client for bucket. The caller owns Close.
func NewGCS(ctx context.Context, bucket string) (*GCS, error) {
	if bucket == "" {
		return nil, errors.New("blob: gcs bucket is empty")
	}
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("blob: gcs client: %w", err)
	}
	return &GCS{client: client, bucket: bucket, httpClient: &http.Client{Timeout: initSessionTimeout}}, nil
}

// Close releases the underlying client.
func (g *GCS) Close() error { return g.client.Close() }

// InitResumableUpload opens the resumable upload session server-side and returns
// the session URI for the client to PUT the object body to. This is the
// documented backend pattern: signed URLs for resumable uploads are generally
// unnecessary when a backend exists — the backend performs the bodyless
// initiation POST and hands the client the session URI, which itself acts as a
// short-lived bearer credential.
//
// The initiation POST must carry no body and Content-Length: 0 (the file bytes
// travel in the client's PUT); it forwards the browser's origin so the provider
// records it on the session and the client's later cross-origin PUT to the
// session URI receives matching CORS headers. The session URI is a bearer
// credential: it is returned to the caller and never logged, and provider
// failures map to neutral errors that carry neither the signed URL nor the URI.
func (g *GCS) InitResumableUpload(ctx context.Context, key, contentType, origin string, _ int64) (Upload, error) {
	signedURL, err := g.signResumableInitURL(key, contentType)
	if err != nil {
		return Upload{}, err
	}
	sessionURI, err := initResumableSession(ctx, g.httpClient, signedURL, contentType, origin)
	if err != nil {
		return Upload{}, err
	}
	headers := map[string]string{}
	if contentType != "" {
		headers["Content-Type"] = contentType
	}
	return Upload{URL: sessionURI, Method: http.MethodPut, Headers: headers}, nil
}

// signResumableInitURL mints the short-lived V4 signed URL the initiation POST is
// issued against. Reusing signing (rather than an authenticated XML-API call)
// keeps the credential path identical to the rest of this file.
func (g *GCS) signResumableInitURL(key, contentType string) (string, error) {
	opts := &storage.SignedURLOptions{
		Scheme:      storage.SigningSchemeV4,
		Method:      http.MethodPost,
		Expires:     time.Now().Add(uploadTTL),
		ContentType: contentType,
		Headers:     []string{resumableStartHeader},
	}
	u, err := g.client.Bucket(g.bucket).SignedURL(key, opts)
	if err != nil {
		return "", fmt.Errorf("blob: sign upload url: %w", err)
	}
	return u, nil
}

// initResumableSession issues the provider-mandated bodyless initiation POST to
// signedURL and returns the session URI from the Location response header. It is
// split out from URL signing so the request shape and error mapping can be
// exercised in unit tests without provider credentials (signing itself is
// covered in staging). See InitResumableUpload for why the body is empty, why
// Content-Length is 0, and why origin is forwarded.
func initResumableSession(ctx context.Context, hc *http.Client, signedURL, contentType, origin string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, signedURL, http.NoBody)
	if err != nil {
		// A NewRequest error can embed the signed URL (a bearer credential); drop
		// it and return a neutral cause.
		return "", errors.New("blob: build resumable init request")
	}
	// The initiation is bodyless with an explicit zero length; a nonzero or
	// absent Content-Length makes the provider reject the init with 400.
	req.ContentLength = 0
	req.Header.Set("x-goog-resumable", "start")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}

	resp, err := hc.Do(req)
	if err != nil {
		// A transport failure returns a *url.Error whose message embeds the full
		// signed URL. Keep only the underlying cause so the credential never
		// reaches the logs (the api layer logs this cause server-side).
		return "", fmt.Errorf("blob: init resumable session: %w", transportCause(err))
	}
	defer func() { _ = resp.Body.Close() }()
	// Never surface the provider body; drain it (bounded) so the connection can be
	// reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxInitErrorBody))

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("blob: init resumable session: unexpected status %d", resp.StatusCode)
	}
	uri := locationHeader(resp.Header)
	if uri == "" {
		return "", errors.New("blob: init resumable session: missing session location")
	}
	return uri, nil
}

// locationHeader returns the Location response header, matched
// case-insensitively. net/http canonicalizes header keys, so Get already handles
// case, but we also scan explicitly so the parse is provably case-insensitive
// regardless of how the provider cased the header on the wire.
func locationHeader(h http.Header) string {
	if v := h.Get("Location"); v != "" {
		return v
	}
	for k, vs := range h {
		if strings.EqualFold(k, "Location") && len(vs) > 0 {
			return vs[0]
		}
	}
	return ""
}

// transportCause unwraps a *url.Error to its underlying cause, dropping the
// request URL (a signed, bearer-credential URL) from the message.
func transportCause(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// Stat returns the object's size, or ErrNotFound.
func (g *GCS) Stat(ctx context.Context, key string) (int64, error) {
	attrs, err := g.client.Bucket(g.bucket).Object(key).Attrs(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("blob: stat: %w", err)
	}
	return attrs.Size, nil
}

// Download streams the object at key into destPath (parent created). The
// pipeline uses it to stage a master into a worker's tmpdir before ffmpeg runs.
func (g *GCS) Download(ctx context.Context, key, destPath string) error {
	rc, err := g.client.Bucket(g.bucket).Object(key).NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("blob: open object reader: %w", err)
	}
	defer func() { _ = rc.Close() }()
	if err := os.MkdirAll(filepath.Dir(destPath), 0o750); err != nil {
		return fmt.Errorf("blob: mkdir: %w", err)
	}
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("blob: create dest: %w", err)
	}
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		return fmt.Errorf("blob: download copy: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("blob: close dest: %w", err)
	}
	return nil
}

// Upload streams the local file at srcPath to the object at key with
// contentType. The pipeline uses it to persist rendered proxy/audio outputs.
func (g *GCS) Upload(ctx context.Context, key, srcPath, contentType string) error {
	f, err := os.Open(srcPath) //nolint:gosec // srcPath is a pipeline-produced temp file.
	if err != nil {
		return fmt.Errorf("blob: open output: %w", err)
	}
	defer func() { _ = f.Close() }()
	w := g.client.Bucket(g.bucket).Object(key).NewWriter(ctx)
	if contentType != "" {
		w.ContentType = contentType
	}
	if _, err := io.Copy(w, f); err != nil {
		_ = w.Close()
		return fmt.Errorf("blob: upload copy: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("blob: finalize upload: %w", err)
	}
	return nil
}

// SignedGetURL returns a short-lived V4 signed GET URL for key.
func (g *GCS) SignedGetURL(_ context.Context, key string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = getTTL
	}
	opts := &storage.SignedURLOptions{
		Scheme:  storage.SigningSchemeV4,
		Method:  "GET",
		Expires: time.Now().Add(ttl),
	}
	u, err := g.client.Bucket(g.bucket).SignedURL(key, opts)
	if err != nil {
		return "", fmt.Errorf("blob: sign get url: %w", err)
	}
	return u, nil
}
