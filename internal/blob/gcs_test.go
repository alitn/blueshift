package blob

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestGCSEmptyBucket is the one thing about the GCS impl unit-testable without
// network: construction rejects an empty bucket. The signing path is exercised in
// staging. The compile-time `var _ Store = (*GCS)(nil)` in gcs.go guarantees the
// impl stays in sync with the interface.
func TestGCSEmptyBucket(t *testing.T) {
	if _, err := NewGCS(t.Context(), ""); err == nil {
		t.Fatal("NewGCS(\"\") = nil err, want rejection")
	}
}

// capturedInit records what the initiation POST looked like on the wire, so the
// tests can prove the provider-mandated shape (bodyless, Content-Length: 0,
// forwarded Origin, resumable-start header).
type capturedInit struct {
	method        string
	contentLength int64
	bodyLen       int
	origin        string
	resumable     string
	contentType   string
}

// initServer stands in for the provider's resumable-initiation endpoint. It
// captures the request into got and replies with status and (unless empty) a
// Location header written under locationKey (so a test can assert case-insensitive
// parsing by using a non-canonical key).
func initServer(t *testing.T, got *capturedInit, status int, locationKey, locationVal string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.method = r.Method
		got.contentLength = r.ContentLength
		got.bodyLen = len(body)
		got.origin = r.Header.Get("Origin")
		got.resumable = r.Header.Get("x-goog-resumable")
		got.contentType = r.Header.Get("Content-Type")
		if locationVal != "" {
			// Assign the map key verbatim so a lowercase key survives to the wire,
			// exercising case-insensitive Location parsing end to end.
			w.Header()[locationKey] = []string{locationVal}
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestInitResumableSessionShape is the AC1 regression: the initiation request
// must be bodyless with Content-Length: 0, forward the browser Origin verbatim,
// and carry the resumable-start + Content-Type headers. The session URI comes
// back from the Location header.
func TestInitResumableSessionShape(t *testing.T) {
	var got capturedInit
	const wantURI = "https://storage.example/session?upload_id=abc123"
	srv := initServer(t, &got, http.StatusCreated, "Location", wantURI)

	uri, err := initResumableSession(context.Background(), srv.Client(), srv.URL, "video/mp4", "https://studio.example.com")
	if err != nil {
		t.Fatalf("initResumableSession: %v", err)
	}
	if uri != wantURI {
		t.Fatalf("session uri = %q, want %q", uri, wantURI)
	}
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if got.bodyLen != 0 {
		t.Errorf("init body = %d bytes, want 0 (bodyless)", got.bodyLen)
	}
	if got.contentLength != 0 {
		t.Errorf("Content-Length = %d, want 0", got.contentLength)
	}
	if got.origin != "https://studio.example.com" {
		t.Errorf("Origin = %q, want the forwarded browser origin", got.origin)
	}
	if got.resumable != "start" {
		t.Errorf("x-goog-resumable = %q, want start", got.resumable)
	}
	if got.contentType != "video/mp4" {
		t.Errorf("Content-Type = %q, want video/mp4", got.contentType)
	}
}

// TestInitResumableSessionOmitsOriginWhenEmpty asserts a non-browser caller
// ("" origin) sends no Origin header rather than an empty one.
func TestInitResumableSessionOmitsOriginWhenEmpty(t *testing.T) {
	var got capturedInit
	srv := initServer(t, &got, http.StatusOK, "Location", "https://storage.example/session")

	if _, err := initResumableSession(context.Background(), srv.Client(), srv.URL, "video/mp4", ""); err != nil {
		t.Fatalf("initResumableSession: %v", err)
	}
	if got.origin != "" {
		t.Errorf("Origin = %q, want empty when no origin is forwarded", got.origin)
	}
}

// TestInitResumableSessionLowercaseLocation proves the Location header is parsed
// case-insensitively: the provider returns it lowercased on the wire.
func TestInitResumableSessionLowercaseLocation(t *testing.T) {
	var got capturedInit
	const wantURI = "https://storage.example/session?upload_id=lower"
	srv := initServer(t, &got, http.StatusCreated, "location", wantURI)

	uri, err := initResumableSession(context.Background(), srv.Client(), srv.URL, "video/mp4", "https://studio.example.com")
	if err != nil {
		t.Fatalf("initResumableSession: %v", err)
	}
	if uri != wantURI {
		t.Fatalf("session uri = %q, want %q (lowercase location must parse)", uri, wantURI)
	}
}

// TestLocationHeaderCaseInsensitive unit-tests the parser directly against
// arbitrarily-cased keys, independent of net/http's on-receive canonicalization.
func TestLocationHeaderCaseInsensitive(t *testing.T) {
	for _, key := range []string{"Location", "location", "LOCATION", "LoCaTiOn"} {
		h := http.Header{key: []string{"https://storage.example/s"}}
		if got := locationHeader(h); got != "https://storage.example/s" {
			t.Errorf("locationHeader with key %q = %q, want the value", key, got)
		}
	}
	if got := locationHeader(http.Header{"X-Other": []string{"v"}}); got != "" {
		t.Errorf("locationHeader with no Location = %q, want empty", got)
	}
}

// TestInitResumableSessionProviderError maps a provider rejection (the live 400)
// to a neutral error that names neither the signed URL nor a session URI.
func TestInitResumableSessionProviderError(t *testing.T) {
	var got capturedInit
	srv := initServer(t, &got, http.StatusBadRequest, "", "")
	// A signature-bearing signed URL is what would be a bearer credential.
	signed := srv.URL + "?X-Goog-Signature=SUPERSECRETSIGNATUREVALUE"

	_, err := initResumableSession(context.Background(), srv.Client(), signed, "video/mp4", "https://studio.example.com")
	if err == nil {
		t.Fatal("initResumableSession() = nil err on 400, want error")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error = %q, want it to note the provider status", err)
	}
	if strings.Contains(err.Error(), "SUPERSECRETSIGNATUREVALUE") {
		t.Errorf("error leaks the signed-URL credential: %q", err)
	}
}

// TestInitResumableSessionMissingLocation treats a success without a session
// Location as a contract break, not a silent empty URI.
func TestInitResumableSessionMissingLocation(t *testing.T) {
	var got capturedInit
	srv := initServer(t, &got, http.StatusCreated, "", "")

	if _, err := initResumableSession(context.Background(), srv.Client(), srv.URL, "video/mp4", ""); err == nil {
		t.Fatal("initResumableSession() = nil err with no Location, want error")
	}
}

// TestInitResumableSessionTransportErrorRedacted asserts a transport failure
// never surfaces the signed URL's signature (the *url.Error message embeds the
// full request URL; transportCause must drop it).
func TestInitResumableSessionTransportErrorRedacted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // connections now refused

	signed := base + "?X-Goog-Signature=SUPERSECRETSIGNATUREVALUE"
	_, err := initResumableSession(context.Background(), &http.Client{Timeout: 2 * time.Second}, signed, "video/mp4", "")
	if err == nil {
		t.Fatal("initResumableSession() = nil err on transport failure, want error")
	}
	if strings.Contains(err.Error(), "SUPERSECRETSIGNATUREVALUE") {
		t.Errorf("transport error leaks the signed-URL credential: %q", err)
	}
}
