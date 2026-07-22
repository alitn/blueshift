// Package blob is the storage seam: everything that puts bytes into, or gets
// bytes out of, object storage goes through the Store interface here. Two
// implementations back it — a production GCS client (gcs.go) and a filesystem
// store used by `make demo`/tests (local.go) so every upload flow can run fully
// offline. The seam exists for that offline requirement; it is deliberately
// small and is not a general storage abstraction.
//
// Storage keys are derived only from public identifiers (the /internal/ids
// encodings) and a sanitized filename, never from internal database ids and
// never from raw client input. The layout is:
//
//	{org}/{episode}/masters/{sanitized-filename}
//
// so every object is unambiguously owned by one org and one episode, and the
// org prefix is the isolation boundary in the bucket.
package blob

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
)

// ErrNotFound is returned by Stat and SignedGetURL when the object does not
// exist. Callers map it to a 4xx (a missing upload is a client-visible "you did
// not finish uploading", not a server fault).
var ErrNotFound = errors.New("blob: object not found")

// Upload is the instruction set the client follows to transfer the master
// bytes. In GCS mode URL is an absolute, narrowly-scoped signed session URL; in
// local mode it is a same-origin relative path served by the app itself. The
// client issues a single request with Method to URL carrying Headers and the
// object body — the shapes are compatible across both backends.
type Upload struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Store is the minimal object-storage contract the app needs in M0.
type Store interface {
	// InitResumableUpload prepares a direct-to-storage upload of key with the
	// given content type and declared size, returning the client instructions.
	InitResumableUpload(ctx context.Context, key, contentType string, sizeBytes int64) (Upload, error)
	// Stat returns the stored object's size in bytes, or ErrNotFound.
	Stat(ctx context.Context, key string) (int64, error)
	// SignedGetURL returns a short-lived URL to read key, or ErrNotFound.
	SignedGetURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// mastersSegment is the fixed sub-prefix under {org}/{episode} for source
// masters. proxiesSegment holds the derived proxy + audio renders; clips/ joins
// them in later milestones.
const (
	mastersSegment = "masters"
	proxiesSegment = "proxies"
)

// maxFilenameLen bounds the sanitized filename component of a key.
const maxFilenameLen = 200

// MasterKey builds the object key for an episode's master upload from the
// already-encoded org and episode public ids (e.g. "org_…", "ep_…") and the
// client-supplied filename. The filename is sanitized to a single safe path
// component; org and episode ids are validated to be plain, separator-free
// tokens so a malformed id can never inject extra path segments. The result is
// stable: the same inputs always produce the same key, so upload-complete can
// recompute it without persisting it.
func MasterKey(orgID, episodeID, filename string) (string, error) {
	if err := validIDToken(orgID); err != nil {
		return "", fmt.Errorf("blob: org id: %w", err)
	}
	if err := validIDToken(episodeID); err != nil {
		return "", fmt.Errorf("blob: episode id: %w", err)
	}
	name, err := SanitizeFilename(filename)
	if err != nil {
		return "", err
	}
	return path.Join(orgID, episodeID, mastersSegment, name), nil
}

// ProxyKey builds the object key for a derived render under an episode's
// proxies/ prefix (the browser proxy and the ASR audio). It mirrors MasterKey:
// org and episode ids are validated as separator-free tokens and the name is
// sanitized to a single safe component, so the same public ids always produce
// the same, org-owned key. name is a fixed, code-supplied filename
// (e.g. "proxy-720p.mp4"), never client input.
func ProxyKey(orgID, episodeID, name string) (string, error) {
	if err := validIDToken(orgID); err != nil {
		return "", fmt.Errorf("blob: org id: %w", err)
	}
	if err := validIDToken(episodeID); err != nil {
		return "", fmt.Errorf("blob: episode id: %w", err)
	}
	clean, err := SanitizeFilename(name)
	if err != nil {
		return "", err
	}
	return path.Join(orgID, episodeID, proxiesSegment, clean), nil
}

// ErrBadFilename means the filename sanitized down to nothing usable (empty,
// all-separators, or path traversal only).
var ErrBadFilename = errors.New("blob: unusable filename")

// ErrBadID means an id token was empty or contained a path separator.
var ErrBadID = errors.New("blob: invalid id token")

func validIDToken(s string) error {
	if s == "" || strings.ContainsAny(s, "/\\") || s == "." || s == ".." {
		return ErrBadID
	}
	return nil
}

// SanitizeFilename reduces an arbitrary client filename to a single safe path
// component: the base name only (any directory portion, including traversal
// like "../../etc/passwd", is dropped), control characters and path separators
// replaced with "_", collapsed and length-capped, extension preserved. It
// rejects inputs that reduce to nothing usable.
func SanitizeFilename(name string) (string, error) {
	// Drop any directory portion from both slash conventions; keep the base.
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(path.Clean("/" + name))
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return "", ErrBadFilename
	}

	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == 0:
			b.WriteByte('_')
		case unicode.IsControl(r):
			b.WriteByte('_')
		case r == ' ':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := collapseUnderscores(b.String())
	out = strings.Trim(out, "._")
	if out == "" {
		return "", ErrBadFilename
	}
	if len(out) > maxFilenameLen {
		out = capPreservingExt(out)
	}
	return out, nil
}

func collapseUnderscores(s string) string {
	var b strings.Builder
	var prevUnderscore bool
	for _, r := range s {
		if r == '_' {
			if prevUnderscore {
				continue
			}
			prevUnderscore = true
		} else {
			prevUnderscore = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// capPreservingExt truncates s to maxFilenameLen while keeping a short trailing
// extension, so a caption/preview by extension still works after truncation.
func capPreservingExt(s string) string {
	ext := path.Ext(s)
	if len(ext) == 0 || len(ext) > 12 {
		return s[:maxFilenameLen]
	}
	keep := maxFilenameLen - len(ext)
	if keep <= 0 {
		return s[:maxFilenameLen]
	}
	return s[:keep] + ext
}
