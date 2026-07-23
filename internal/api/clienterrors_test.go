package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// bufLogger returns a JSON logger writing to a buffer so tests can inspect the
// emitted log lines.
func bufLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

func postClientError(t *testing.T, h http.Handler, ip, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, ClientErrorsPath, strings.NewReader(body))
	req.RemoteAddr = ip + ":1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// logLines returns the parsed JSON log entries in buf.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line not JSON: %v (%q)", err, line)
		}
		out = append(out, m)
	}
	return out
}

func TestClientErrorLogsNeutralFields(t *testing.T) {
	logger, buf := bufLogger()
	h := newRouter(t, Deps{Logger: logger})

	body := `{"message":"TypeError: x is not a function","stack":"at foo (app.js:10:5)","url":"https://app.example/login","line":10,"col":5,"user_agent":"Mozilla/5.0"}`
	rec := postClientError(t, h, "10.0.0.1", body)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("expected empty body, got %q", rec.Body.String())
	}

	lines := logLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}
	e := lines[0]
	if e["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", e["level"])
	}
	if e["msg"] != "client error" {
		t.Errorf("msg = %v, want 'client error'", e["msg"])
	}
	if e["client_message"] != "TypeError: x is not a function" {
		t.Errorf("client_message = %v", e["client_message"])
	}
	if e["client_url"] != "https://app.example/login" {
		t.Errorf("client_url = %v", e["client_url"])
	}
	if e["client_stack"] != "at foo (app.js:10:5)" {
		t.Errorf("client_stack = %v", e["client_stack"])
	}
	// slog encodes ints as JSON numbers -> float64 after decode.
	if e["client_line"] != float64(10) || e["client_col"] != float64(5) {
		t.Errorf("line/col = %v/%v, want 10/5", e["client_line"], e["client_col"])
	}
	if e["client_user_agent"] != "Mozilla/5.0" {
		t.Errorf("client_user_agent = %v", e["client_user_agent"])
	}
	if id, _ := e["client_event_id"].(string); id == "" {
		t.Error("client_event_id missing or empty")
	}
	// Every logged field is client_*-prefixed or a standard slog key; nothing
	// exposes an internal identifier.
	for k := range e {
		switch k {
		case "time", "level", "msg":
			continue
		}
		if !strings.HasPrefix(k, "client_") {
			t.Errorf("unexpected non-client_ log field %q", k)
		}
	}
}

func TestClientErrorTruncatesLongFields(t *testing.T) {
	logger, buf := bufLogger()
	h := newRouter(t, Deps{Logger: logger})

	longMsg := strings.Repeat("a", maxClientErrorMessage+500)
	longStack := strings.Repeat("b", maxClientErrorStack+500)
	body, err := json.Marshal(clientErrorReport{Message: longMsg, Stack: longStack, URL: "u"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	rec := postClientError(t, h, "10.0.0.2", string(body))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}

	e := logLines(t, buf)[0]
	if got := len(e["client_message"].(string)); got != maxClientErrorMessage {
		t.Errorf("client_message len = %d, want %d (truncated, not rejected)", got, maxClientErrorMessage)
	}
	if got := len(e["client_stack"].(string)); got != maxClientErrorStack {
		t.Errorf("client_stack len = %d, want %d", got, maxClientErrorStack)
	}
}

func TestClientErrorTruncatePreservesUTF8(t *testing.T) {
	// A run of multibyte runes whose byte length straddles the cap must not be
	// split mid-rune (Persian content is a first-class case here).
	s := strings.Repeat("م", 1200) // 2 bytes each -> 2400 bytes > 2000 cap
	got := truncateUTF8(s, maxClientErrorMessage)
	if len(got) > maxClientErrorMessage {
		t.Fatalf("len = %d, want <= %d", len(got), maxClientErrorMessage)
	}
	if !utf8.ValidString(got) {
		t.Error("truncation produced invalid UTF-8")
	}
}

func TestClientErrorMalformedBodyIs204(t *testing.T) {
	logger, buf := bufLogger()
	h := newRouter(t, Deps{Logger: logger})

	for _, body := range []string{`not json`, `{"message":`, ``, `{}`, `{"message":"   "}`} {
		rec := postClientError(t, h, "10.0.0.3", body)
		if rec.Code != http.StatusNoContent {
			t.Errorf("body %q -> status %d, want 204", body, rec.Code)
		}
	}
	// None of the above carries a usable message, so nothing is logged.
	if lines := logLines(t, buf); len(lines) != 0 {
		t.Errorf("expected no log lines for malformed/empty bodies, got %d", len(lines))
	}
}

func TestClientErrorRateLimited(t *testing.T) {
	logger, buf := bufLogger()
	// Fixed clock (via newRouter default Now) so no tokens refill mid-test.
	h := newRouter(t, Deps{Logger: logger})

	for i := 0; i < clientErrRatePerMin+3; i++ {
		rec := postClientError(t, h, "10.0.0.4", `{"message":"boom","url":"u"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("request %d status = %d, want 204 (endpoint never errors)", i+1, rec.Code)
		}
	}
	// Exactly clientErrRatePerMin reports are logged; the rest are dropped.
	if got := len(logLines(t, buf)); got != clientErrRatePerMin {
		t.Errorf("logged %d reports, want %d (over-limit dropped, still 204)", got, clientErrRatePerMin)
	}
}

func TestClientErrorPerIPBuckets(t *testing.T) {
	logger, buf := bufLogger()
	h := newRouter(t, Deps{Logger: logger})

	// Exhaust one IP's bucket.
	for i := 0; i < clientErrRatePerMin+2; i++ {
		postClientError(t, h, "10.0.0.5", `{"message":"a","url":"u"}`)
	}
	// A different IP is unaffected.
	rec := postClientError(t, h, "10.0.0.6", `{"message":"b","url":"u"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("distinct IP status = %d, want 204", rec.Code)
	}
	if got := len(logLines(t, buf)); got != clientErrRatePerMin+1 {
		t.Errorf("logged %d, want %d (per-IP buckets)", got, clientErrRatePerMin+1)
	}
}

func TestClientErrorFallsBackToRequestUserAgent(t *testing.T) {
	logger, buf := bufLogger()
	h := newRouter(t, Deps{Logger: logger})

	req := httptest.NewRequest(http.MethodPost, ClientErrorsPath, strings.NewReader(`{"message":"boom","url":"u"}`))
	req.RemoteAddr = "10.0.0.7:1"
	req.Header.Set("User-Agent", "HeaderUA/1.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if e := logLines(t, buf)[0]; e["client_user_agent"] != "HeaderUA/1.0" {
		t.Errorf("client_user_agent = %v, want request header fallback", e["client_user_agent"])
	}
}
