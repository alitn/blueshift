package blob

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/storage"
)

// resumableStartHeader is the header that turns a signed POST into a resumable
// upload session initiation; the response Location is the session URL the client
// then PUTs the bytes to.
const resumableStartHeader = "x-goog-resumable:start"

// uploadTTL / getTTL bound how long a signed URL stays valid.
const (
	uploadTTL = 6 * time.Hour
	getTTL    = 1 * time.Hour
)

// GCS is the production Store. Signing uses the credentials the client is
// constructed with; on Cloud Run that is the service account resolved from the
// metadata server, whose SignBlob capability the storage client uses to produce
// V4 signatures — no private key file ever lives in the repo or image.
//
// Network behaviour is exercised in staging, not in unit tests; the unit build
// only compiles this file and asserts it satisfies Store.
type GCS struct {
	client *storage.Client
	bucket string
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
	return &GCS{client: client, bucket: bucket}, nil
}

// Close releases the underlying client.
func (g *GCS) Close() error { return g.client.Close() }

// InitResumableUpload returns a signed POST URL that initiates a resumable
// upload session for key. The client POSTs with the returned headers, reads the
// session URL from the Location response header, then PUTs the object body.
func (g *GCS) InitResumableUpload(_ context.Context, key, contentType string, _ int64) (Upload, error) {
	opts := &storage.SignedURLOptions{
		Scheme:      storage.SigningSchemeV4,
		Method:      "POST",
		Expires:     time.Now().Add(uploadTTL),
		ContentType: contentType,
		Headers:     []string{resumableStartHeader},
	}
	u, err := g.client.Bucket(g.bucket).SignedURL(key, opts)
	if err != nil {
		return Upload{}, fmt.Errorf("blob: sign upload url: %w", err)
	}
	headers := map[string]string{"x-goog-resumable": "start"}
	if contentType != "" {
		headers["Content-Type"] = contentType
	}
	return Upload{URL: u, Method: "POST", Headers: headers}, nil
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
