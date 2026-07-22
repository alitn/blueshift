package logx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestSeverityMapping(t *testing.T) {
	cases := map[slog.Level]string{
		slog.LevelDebug: "DEBUG",
		slog.LevelInfo:  "INFO",
		slog.LevelWarn:  "WARNING",
		slog.LevelError: "ERROR",
	}
	for lvl, want := range cases {
		if got := severity(lvl); got != want {
			t.Errorf("severity(%v) = %q, want %q", lvl, got, want)
		}
	}
}

func TestNewEmitsCloudLoggingKeys(t *testing.T) {
	var buf bytes.Buffer
	logger := New(slog.LevelInfo, &buf)
	logger.Warn("hello", "k", "v")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal log line: %v (line=%q)", err, buf.String())
	}

	if entry["severity"] != "WARNING" {
		t.Errorf("severity = %v, want WARNING", entry["severity"])
	}
	if entry["message"] != "hello" {
		t.Errorf("message = %v, want hello", entry["message"])
	}
	if _, ok := entry["time"]; !ok {
		t.Errorf("missing time key: %v", entry)
	}
	// Ensure the default slog keys were renamed, not duplicated.
	if _, ok := entry["level"]; ok {
		t.Errorf("unexpected raw level key: %v", entry)
	}
	if _, ok := entry["msg"]; ok {
		t.Errorf("unexpected raw msg key: %v", entry)
	}
	if entry["k"] != "v" {
		t.Errorf("custom attr k = %v, want v", entry["k"])
	}
}

func TestNewRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := New(slog.LevelWarn, &buf)
	logger.Info("suppressed")
	if buf.Len() != 0 {
		t.Errorf("info line emitted at warn level: %q", buf.String())
	}
}
