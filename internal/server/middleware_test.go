package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"blueshift/internal/logx"
)

func TestRequestLoggerFields(t *testing.T) {
	var buf bytes.Buffer
	logger := logx.New(slog.LevelInfo, &buf)

	h := requestLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/some/path", nil))

	if rec.Code != http.StatusTeapot {
		t.Fatalf("downstream status not propagated: %d", rec.Code)
	}

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal log: %v (line=%q)", err, buf.String())
	}
	if entry["message"] != "request" {
		t.Errorf("message = %v, want request", entry["message"])
	}
	if entry["method"] != http.MethodPost {
		t.Errorf("method = %v, want POST", entry["method"])
	}
	if entry["path"] != "/some/path" {
		t.Errorf("path = %v, want /some/path", entry["path"])
	}
	if entry["status"] != float64(http.StatusTeapot) {
		t.Errorf("status = %v, want 418", entry["status"])
	}
	if _, ok := entry["duration_ms"]; !ok {
		t.Errorf("missing duration_ms: %v", entry)
	}
}

func TestRecovererCatchesPanic(t *testing.T) {
	var buf bytes.Buffer
	logger := logx.New(slog.LevelInfo, &buf)

	h := recoverer(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("kaboom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal log: %v (line=%q)", err, buf.String())
	}
	if entry["severity"] != "ERROR" {
		t.Errorf("severity = %v, want ERROR", entry["severity"])
	}
	if entry["message"] != "panic recovered" {
		t.Errorf("message = %v, want panic recovered", entry["message"])
	}
}

func TestRequestLoggerLogsRecoveredPanicAs500(t *testing.T) {
	var buf bytes.Buffer
	logger := logx.New(slog.LevelInfo, &buf)

	// Ordering mirrors server.New: logger outermost, recoverer inside.
	h := chain(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("x") }),
		requestLogger(logger),
		recoverer(logger),
	)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	// Two log lines: the ERROR panic line and the INFO request line at 500.
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	var sawRequest500 bool
	for {
		var entry map[string]any
		if err := dec.Decode(&entry); err != nil {
			break
		}
		if entry["message"] == "request" && entry["status"] == float64(500) {
			sawRequest500 = true
		}
	}
	if !sawRequest500 {
		t.Errorf("expected request log line at status 500, got: %q", buf.String())
	}
}
