package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// ClientErrorsPath is the public (unauthenticated) endpoint the browser posts
// uncaught errors to so they land in structured server logs. It is not an auth
// concept, but like the login route it is whitelisted by the server's deny-by-
// default gate; the constant lives here (with its handler) and the gate imports
// it so both register exactly the same string.
const ClientErrorsPath = "/api/client-errors"

// Client error caps. Fields are truncated to these lengths, never rejected for
// being too long — a length error must not stop us from recording the report.
const (
	maxClientErrorBody    = 1 << 16 // whole request body ceiling (comfortably above the field caps)
	maxClientErrorMessage = 2000
	maxClientErrorStack   = 8000
	maxClientErrorURL     = 2048
	maxClientErrorUA      = 512
)

// clientErrRatePerMin caps client-error reports per client IP per minute. This
// endpoint is unauthenticated, so the token bucket is the only abuse control.
const clientErrRatePerMin = 10

// clientErrorReport is the neutral report shape the browser posts. Every field
// carries only what the browser already put in the error itself. Line/col are
// pointers so an absent value is distinguishable from a real zero and simply
// omitted from the log.
type clientErrorReport struct {
	Message   string `json:"message"`
	Stack     string `json:"stack"`
	URL       string `json:"url"`
	Line      *int   `json:"line"`
	Col       *int   `json:"col"`
	UserAgent string `json:"user_agent"`
}

// clientError records an uncaught browser error into the structured log at ERROR
// severity with client_* fields plus a random event id. It ALWAYS responds 204,
// even on a malformed body or when rate limited: this endpoint exists to absorb
// errors and must never generate its own error loop (a 4xx/5xx here would make
// the browser's error handler fire again on the failed forward).
func (h *handler) clientError(w http.ResponseWriter, r *http.Request) {
	// Over-limit reports are dropped silently — still 204, never logged, so a
	// misbehaving client cannot flood the log or trigger retry storms.
	if !h.clientErrLimiter.allow(clientIP(r)) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var rep clientErrorReport
	if err := json.NewDecoder(io.LimitReader(r.Body, maxClientErrorBody)).Decode(&rep); err != nil {
		// Malformed/oversized body: swallow it. Nothing actionable to log.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	msg := truncateUTF8(strings.TrimSpace(rep.Message), maxClientErrorMessage)
	if msg == "" {
		// No message means no useful record; don't emit an empty ERROR line.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ua := rep.UserAgent
	if ua == "" {
		ua = r.UserAgent()
	}

	attrs := []slog.Attr{
		slog.String("client_event_id", errorID()),
		slog.String("client_message", msg),
	}
	if url := truncateUTF8(rep.URL, maxClientErrorURL); url != "" {
		attrs = append(attrs, slog.String("client_url", url))
	}
	if stack := truncateUTF8(rep.Stack, maxClientErrorStack); stack != "" {
		attrs = append(attrs, slog.String("client_stack", stack))
	}
	if rep.Line != nil {
		attrs = append(attrs, slog.Int("client_line", *rep.Line))
	}
	if rep.Col != nil {
		attrs = append(attrs, slog.Int("client_col", *rep.Col))
	}
	if ua != "" {
		attrs = append(attrs, slog.String("client_user_agent", truncateUTF8(ua, maxClientErrorUA)))
	}

	h.deps.Logger.LogAttrs(r.Context(), slog.LevelError, "client error", attrs...)
	w.WriteHeader(http.StatusNoContent)
}

// truncateUTF8 caps s to at most max bytes without splitting a multibyte rune,
// so an error message in any script (e.g. Persian) stays valid UTF-8 in the log.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// newClientErrLimiter builds the client-error token bucket. now may be nil.
func newClientErrLimiter(now func() time.Time) *rateLimiter {
	return newRateLimiter(clientErrRatePerMin, time.Minute, now)
}
