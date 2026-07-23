package config

import (
	"log/slog"
	"testing"
	"time"
)

// env builds a getenv func backed by a fixed map.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil {
		t.Fatalf("load: unexpected error: %v", err)
	}
	if cfg.Port != defaultPort {
		t.Errorf("Port = %q, want %q", cfg.Port, defaultPort)
	}
	if cfg.Env != EnvDev {
		t.Errorf("Env = %q, want %q", cfg.Env, EnvDev)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want Info", cfg.LogLevel)
	}
	if got, want := cfg.Addr(), ":8080"; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
	// Dev defaults to the offline local blob store with a temp-dir root.
	if cfg.BlobMode != BlobModeLocal {
		t.Errorf("BlobMode = %q, want local", cfg.BlobMode)
	}
	if cfg.BlobDir == "" {
		t.Error("BlobDir empty in local mode, want a default root")
	}
}

func TestLoadBlobInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"invalid blob mode":       {"BLOB_MODE": "s3"},
		"gcs missing bucket":      {"BLOB_MODE": "gcs"},
		"local blob mode in prod": {"ENV": "prod", "SESSION_SECRET": "x", "IDP_API_KEY": "k", "BLOB_MODE": "local"},
		"prod missing bucket":     {"ENV": "prod", "SESSION_SECRET": "x", "IDP_API_KEY": "k"},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := load(env(m)); err == nil {
				t.Fatalf("load(%v): expected error, got nil", m)
			}
		})
	}
}

func TestLoadBlobLocalOverride(t *testing.T) {
	cfg, err := load(env(map[string]string{"BLOB_MODE": "local", "BLOB_DIR": "/tmp/custom-blob"}))
	if err != nil {
		t.Fatalf("load: unexpected error: %v", err)
	}
	if cfg.BlobMode != BlobModeLocal || cfg.BlobDir != "/tmp/custom-blob" {
		t.Errorf("blob = %q dir=%q", cfg.BlobMode, cfg.BlobDir)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"PORT":           "9090",
		"ENV":            "prod",
		"LOG_LEVEL":      "warning",
		"DATABASE_URL":   "postgres://u:p@h:5432/db",
		"SESSION_SECRET": "prod-secret",
		"IDP_API_KEY":    "prod-key",
		"GCS_BUCKET":     "bs-masters",
	}))
	if err != nil {
		t.Fatalf("load: unexpected error: %v", err)
	}
	if cfg.BlobMode != BlobModeGCS || cfg.GCSBucket != "bs-masters" {
		t.Errorf("blob = %q bucket=%q, want gcs + bs-masters", cfg.BlobMode, cfg.GCSBucket)
	}
	if cfg.Port != "9090" {
		t.Errorf("Port = %q, want 9090", cfg.Port)
	}
	if cfg.Env != EnvProd {
		t.Errorf("Env = %q, want prod", cfg.Env)
	}
	if cfg.LogLevel != slog.LevelWarn {
		t.Errorf("LogLevel = %v, want Warn", cfg.LogLevel)
	}
	if cfg.DatabaseURL != "postgres://u:p@h:5432/db" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	// prod derives identity mode and must not fall back to the dev secret.
	if cfg.AuthMode != AuthModeIdentity {
		t.Errorf("AuthMode = %q, want identity", cfg.AuthMode)
	}
	if cfg.SessionSecret != "prod-secret" || cfg.SessionSecretDefaulted {
		t.Errorf("SessionSecret = %q defaulted=%v, want explicit", cfg.SessionSecret, cfg.SessionSecretDefaulted)
	}
}

func TestLoadAuthDevDefaults(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil {
		t.Fatalf("load: unexpected error: %v", err)
	}
	if cfg.AuthMode != AuthModeDev {
		t.Errorf("AuthMode = %q, want dev", cfg.AuthMode)
	}
	if cfg.SessionSecret != DevSessionSecret || !cfg.SessionSecretDefaulted {
		t.Errorf("dev secret = %q defaulted=%v, want dev default flagged", cfg.SessionSecret, cfg.SessionSecretDefaulted)
	}
	if cfg.DevPassword != defaultDevPassword {
		t.Errorf("DevPassword = %q, want %q", cfg.DevPassword, defaultDevPassword)
	}
}

func TestLoadAuthOverrides(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"AUTH_MODE":      "identity",
		"SESSION_SECRET": "s3cret",
		"DEV_PASSWORD":   "hunter2",
		"IDP_API_KEY":    "abc123",
	}))
	if err != nil {
		t.Fatalf("load: unexpected error: %v", err)
	}
	if cfg.AuthMode != AuthModeIdentity {
		t.Errorf("AuthMode = %q, want identity", cfg.AuthMode)
	}
	if cfg.SessionSecret != "s3cret" || cfg.SessionSecretDefaulted {
		t.Errorf("SessionSecret = %q defaulted=%v", cfg.SessionSecret, cfg.SessionSecretDefaulted)
	}
	if cfg.DevPassword != "hunter2" {
		t.Errorf("DevPassword = %q, want hunter2", cfg.DevPassword)
	}
	if cfg.IDPAPIKey != "abc123" {
		t.Errorf("IDPAPIKey = %q, want abc123", cfg.IDPAPIKey)
	}
}

func TestLoadAuthInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"invalid auth mode":         {"AUTH_MODE": "sso"},
		"dev mode in prod":          {"ENV": "prod", "SESSION_SECRET": "x", "AUTH_MODE": "dev"},
		"prod missing secret":       {"ENV": "prod", "IDP_API_KEY": "k"},
		"identity missing key":      {"AUTH_MODE": "identity"},
		"staging missing secret":    {"ENV": "staging", "IDP_API_KEY": "k"},
		"prod identity missing key": {"ENV": "prod", "SESSION_SECRET": "x"},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := load(env(m)); err == nil {
				t.Fatalf("load(%v): expected error, got nil", m)
			}
		})
	}
}

func TestDatabaseURLOptional(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil {
		t.Fatalf("load: unexpected error: %v", err)
	}
	if cfg.DatabaseURL != "" {
		t.Errorf("DatabaseURL = %q, want empty when unset", cfg.DatabaseURL)
	}
}

func TestLoadInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"port not a number": {"PORT": "abc"},
		"port zero":         {"PORT": "0"},
		"port out of range": {"PORT": "70000"},
		"unknown env":       {"ENV": "production"},
		"unknown log level": {"LOG_LEVEL": "verbose"},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := load(env(m)); err == nil {
				t.Fatalf("load(%v): expected error, got nil", m)
			}
		})
	}
}

func TestLoadWorkerDefaults(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.WorkerTrigger != WorkerTriggerExec {
		t.Errorf("WorkerTrigger = %q, want exec", cfg.WorkerTrigger)
	}
	if cfg.IngestTimeout != defaultIngestTimeout {
		t.Errorf("IngestTimeout = %v, want %v", cfg.IngestTimeout, defaultIngestTimeout)
	}
}

func TestLoadWorkerCloudRun(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"WORKER_TRIGGER":     "cloudrun",
		"WORKER_JOB_REGION":  "us-central1",
		"WORKER_JOB_PROJECT": "bs-proj",
		"WORKER_JOB_NAME":    "bs-worker",
		"INGEST_TIMEOUT":     "45m",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.WorkerTrigger != WorkerTriggerCloudRun {
		t.Errorf("WorkerTrigger = %q, want cloudrun", cfg.WorkerTrigger)
	}
	if cfg.WorkerJobRegion != "us-central1" || cfg.WorkerJobProject != "bs-proj" || cfg.WorkerJobName != "bs-worker" {
		t.Errorf("job coords = %q/%q/%q", cfg.WorkerJobRegion, cfg.WorkerJobProject, cfg.WorkerJobName)
	}
	if cfg.IngestTimeout != 45*time.Minute {
		t.Errorf("IngestTimeout = %v, want 45m", cfg.IngestTimeout)
	}
}

func TestLoadWorkerInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"invalid trigger":          {"WORKER_TRIGGER": "lambda"},
		"cloudrun missing coords":  {"WORKER_TRIGGER": "cloudrun"},
		"cloudrun partial coords":  {"WORKER_TRIGGER": "cloudrun", "WORKER_JOB_REGION": "r"},
		"bad ingest timeout":       {"INGEST_TIMEOUT": "soon"},
		"nonpositive ingest tmout": {"INGEST_TIMEOUT": "0s"},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := load(env(m)); err == nil {
				t.Fatalf("load(%v): expected error, got nil", m)
			}
		})
	}
}

func TestLoadSweepDefaults(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SweepInterval != defaultSweepInterval {
		t.Errorf("SweepInterval = %v, want %v", cfg.SweepInterval, defaultSweepInterval)
	}
	if cfg.UploadTTL != defaultUploadTTL {
		t.Errorf("UploadTTL = %v, want %v", cfg.UploadTTL, defaultUploadTTL)
	}
}

func TestLoadSweepOverrides(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"SWEEP_INTERVAL": "2s",
		"UPLOAD_TTL":     "1s",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SweepInterval != 2*time.Second {
		t.Errorf("SweepInterval = %v, want 2s", cfg.SweepInterval)
	}
	if cfg.UploadTTL != time.Second {
		t.Errorf("UploadTTL = %v, want 1s", cfg.UploadTTL)
	}
}

func TestLoadSweepInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"bad sweep interval":     {"SWEEP_INTERVAL": "often"},
		"nonpositive interval":   {"SWEEP_INTERVAL": "0s"},
		"negative interval":      {"SWEEP_INTERVAL": "-1h"},
		"bad upload ttl":         {"UPLOAD_TTL": "forever"},
		"nonpositive upload ttl": {"UPLOAD_TTL": "0s"},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := load(env(m)); err == nil {
				t.Fatalf("load(%v): expected error, got nil", m)
			}
		})
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"INFO":    slog.LevelInfo,
		"Warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for in, want := range cases {
		got, err := parseLevel(in)
		if err != nil {
			t.Errorf("parseLevel(%q): unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}
