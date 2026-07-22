// Package api implements the app's JSON HTTP endpoints. In M0 that is the auth
// surface: login, logout, and me. Every DTO here is deliberately neutral — the
// vendor-leak gate greps this package, and provider details are classified into
// neutral errors at the internal/auth boundary before they ever reach here.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strings"
)

// writeJSON encodes v as the response body with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errBody is the neutral error envelope: a short machine code, no prose that
// could hint at the underlying stack.
type errBody struct {
	Error string `json:"error"`
}

// errIDBody adds an internal correlation id for server-side ("unavailable")
// failures so support can find the matching log line. The id itself is a random
// hex string — it names nothing.
type errIDBody struct {
	Error   string `json:"error"`
	ErrorID string `json:"error_id"`
}

// errorID returns a short random hex id for correlating a client error with the
// server log line that holds the raw cause.
func errorID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// clientIP extracts a best-effort client IP for rate limiting. Behind the Cloud
// Run proxy the real client is the first X-Forwarded-For hop; otherwise the
// transport remote address. Rate limiting is a light abuse control, not a
// security boundary, so a spoofable header is acceptable here.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
