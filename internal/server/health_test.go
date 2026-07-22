package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	healthz(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %q, want ok", body["status"])
	}
}

func TestReadyzEmpty(t *testing.T) {
	rd := NewReadiness()
	h := rd.readyzHandler(discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// Exact shape matters for the contract: checks must serialize as {}.
	if got := rec.Body.String(); got != `{"status":"ready","checks":{}}`+"\n" {
		t.Errorf("body = %q, want ready with empty checks", got)
	}
}

func TestReadyzPassingCheck(t *testing.T) {
	rd := NewReadiness()
	rd.Register("db", func(context.Context) error { return nil })
	h := rd.readyzHandler(discardLogger())

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp readyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "ready" || resp.Checks["db"] != "ok" {
		t.Errorf("resp = %+v, want ready with db ok", resp)
	}
}

func TestReadyzFailingCheck(t *testing.T) {
	rd := NewReadiness()
	rd.Register("db", func(context.Context) error { return errors.New("boom") })
	rd.Register("storage", func(context.Context) error { return nil })
	h := rd.readyzHandler(discardLogger())

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var resp readyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "unavailable" {
		t.Errorf("status = %q, want unavailable", resp.Status)
	}
	if resp.Checks["db"] != "failed" {
		t.Errorf("db check = %q, want failed", resp.Checks["db"])
	}
	if resp.Checks["storage"] != "ok" {
		t.Errorf("storage check = %q, want ok", resp.Checks["storage"])
	}
	// The raw error must not leak into the client-visible payload.
	if body := rec.Body.String(); contains(body, "boom") {
		t.Errorf("raw error leaked into response: %q", body)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
