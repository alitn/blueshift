// Package config loads the app server's runtime configuration from the
// environment. Secret Manager values are injected as env vars by Cloud Run
// (--set-secrets); there is no Secret Manager client here.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func getenvFromOS(key string) string { return os.Getenv(key) }

// Env is the deployment environment. It is a small closed set validated at load
// time so a typo fails fast instead of silently behaving like production.
type Env string

const (
	EnvDev     Env = "dev"
	EnvStaging Env = "staging"
	EnvProd    Env = "prod"
)

// AuthMode selects the login backend. `dev` verifies a shared offline password
// (used by `make demo`); `identity` calls the identity provider server-side.
type AuthMode string

const (
	AuthModeDev      AuthMode = "dev"
	AuthModeIdentity AuthMode = "identity"
)

// BlobMode selects the object-storage backend. `local` writes under BLOB_DIR and
// serves uploads from the app itself (used by `make demo`); `gcs` uses the
// bucket named by GCS_BUCKET with credentials from the runtime.
type BlobMode string

const (
	BlobModeLocal BlobMode = "local"
	BlobModeGCS   BlobMode = "gcs"
)

// WorkerTrigger selects how the API server launches the pipeline worker after an
// upload completes. `exec` spawns the local worker binary (dev/demo); `cloudrun`
// starts a Cloud Run Jobs execution over the platform's admin REST API.
type WorkerTrigger string

const (
	WorkerTriggerExec     WorkerTrigger = "exec"
	WorkerTriggerCloudRun WorkerTrigger = "cloudrun"
)

// Config is the fully-resolved, validated server configuration.
type Config struct {
	// Port is the TCP port the HTTP server binds to.
	Port string
	// Env is the deployment environment (dev|staging|prod).
	Env Env
	// LogLevel is the minimum slog level emitted to stdout.
	LogLevel slog.Level
	// DatabaseURL is the Postgres DSN. Empty means no database is configured:
	// the app still boots, and the /readyz "db" check is not registered.
	DatabaseURL string

	// PublicBaseURL is the app's public base URL (e.g. https://app.example.com).
	// It is the fallback Origin the server forwards when it opens a
	// direct-to-storage upload session and the create request carried no Origin
	// header. Optional: browsers always send Origin on the create POST, so this
	// only affects non-browser callers, for whom CORS does not apply. Unset means
	// no fallback origin is forwarded.
	PublicBaseURL string

	// AuthMode selects the login backend (dev|identity).
	AuthMode AuthMode
	// SessionSecret keys the HMAC that signs session cookies.
	SessionSecret string
	// SessionSecretDefaulted is true when SessionSecret fell back to the
	// insecure dev default (SESSION_SECRET was unset in dev); the caller emits
	// a startup WARN.
	SessionSecretDefaulted bool
	// DevPassword is the shared password accepted in dev auth mode.
	DevPassword string
	// IDPAPIKey is the identity provider web API key (identity mode only).
	IDPAPIKey string

	// BlobMode selects the object-storage backend (local|gcs).
	BlobMode BlobMode
	// GCSBucket is the storage bucket name (gcs mode only).
	GCSBucket string
	// BlobDir is the filesystem root for the local blob store (local mode).
	BlobDir string

	// WorkerTrigger selects how upload-complete launches the pipeline worker
	// (exec|cloudrun). Defaults to exec in dev, cloudrun in staging/prod.
	WorkerTrigger WorkerTrigger
	// WorkerBin is the path to the worker binary the exec trigger spawns.
	// Required only when WorkerTrigger=exec and a trigger is actually fired.
	WorkerBin string
	// WorkerJobRegion is the Cloud region of the worker Job (cloudrun trigger).
	WorkerJobRegion string
	// WorkerJobProject is the Cloud project id hosting the worker Job (cloudrun).
	WorkerJobProject string
	// WorkerJobName is the Cloud Run Job resource name to execute (cloudrun).
	WorkerJobName string

	// IngestTimeout bounds a single ingest stage attempt in the worker.
	IngestTimeout time.Duration

	// SweepInterval is the cadence of the abandoned-upload sweep (the app-side
	// TTL reaper). Defaults to 1h. The sweep only runs when a database is
	// configured (DATABASE_URL set).
	SweepInterval time.Duration
	// UploadTTL is how long a created-but-never-uploaded episode may sit at
	// 'uploaded' with no master key before the sweep removes it. Defaults to 6h.
	UploadTTL time.Duration
}

// Addr returns the listen address (":<port>") for http.Server.
func (c Config) Addr() string { return ":" + c.Port }

// Default values applied when the corresponding env var is unset or empty.
const (
	defaultPort     = "8080"
	defaultEnv      = EnvDev
	defaultLogLevel = slog.LevelInfo

	// DevSessionSecret is the insecure fallback signing key used only when
	// SESSION_SECRET is unset in dev. Real deployments must set the env var.
	DevSessionSecret = "blueshift-dev-insecure-session-secret"
	// defaultDevPassword is the dev auth-mode password when DEV_PASSWORD is
	// unset. Matches `make demo` expectations.
	defaultDevPassword = "blueshift-dev"

	// defaultBlobDirName is the local blob root under the OS temp dir when
	// BLOB_DIR is unset in local mode.
	defaultBlobDirName = "blueshift-blob"

	// defaultIngestTimeout bounds a single ingest stage attempt when
	// INGEST_TIMEOUT is unset.
	defaultIngestTimeout = 30 * time.Minute

	// defaultSweepInterval is the cadence of the abandoned-upload sweep when
	// SWEEP_INTERVAL is unset.
	defaultSweepInterval = time.Hour
	// defaultUploadTTL is how long an abandoned (created-but-never-uploaded)
	// episode may linger before the sweep removes it, when UPLOAD_TTL is unset.
	defaultUploadTTL = 6 * time.Hour
)

// Load reads configuration from the process environment.
func Load() (Config, error) {
	return load(getenvFromOS)
}

// load is the testable core: it reads values through the supplied getenv so
// tests never touch the real process environment.
func load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Port:     defaultPort,
		Env:      defaultEnv,
		LogLevel: defaultLogLevel,
	}

	if v := strings.TrimSpace(getenv("PORT")); v != "" {
		if !validPort(v) {
			return Config{}, fmt.Errorf("config: invalid PORT %q (want 1-65535)", v)
		}
		cfg.Port = v
	}

	if v := strings.TrimSpace(getenv("ENV")); v != "" {
		e := Env(v)
		switch e {
		case EnvDev, EnvStaging, EnvProd:
			cfg.Env = e
		default:
			return Config{}, fmt.Errorf("config: invalid ENV %q (want dev|staging|prod)", v)
		}
	}

	if v := strings.TrimSpace(getenv("LOG_LEVEL")); v != "" {
		lvl, err := parseLevel(v)
		if err != nil {
			return Config{}, err
		}
		cfg.LogLevel = lvl
	}

	// DATABASE_URL is optional in this milestone: unset is a valid state where
	// the database readiness check is simply not wired up.
	cfg.DatabaseURL = strings.TrimSpace(getenv("DATABASE_URL"))

	// PUBLIC_BASE_URL is optional: it is only the fallback upload-session Origin
	// for non-browser callers (browsers always send Origin on the create POST).
	cfg.PublicBaseURL = strings.TrimSpace(getenv("PUBLIC_BASE_URL"))

	if err := loadAuth(&cfg, getenv); err != nil {
		return Config{}, err
	}

	if err := loadBlob(&cfg, getenv); err != nil {
		return Config{}, err
	}

	if err := loadWorker(&cfg, getenv); err != nil {
		return Config{}, err
	}

	if err := loadSweep(&cfg, getenv); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// loadSweep resolves the abandoned-upload sweep cadence and TTL. Both default to
// production-safe values (1h cadence, 6h TTL) and accept any positive Go
// duration (e.g. "2s", "90m") so a transient env can drive a fast sweep for
// verification. The sweep itself is wired only when a database is configured.
func loadSweep(cfg *Config, getenv func(string) string) error {
	cfg.SweepInterval = defaultSweepInterval
	if v := strings.TrimSpace(getenv("SWEEP_INTERVAL")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("config: invalid SWEEP_INTERVAL %q: %w", v, err)
		}
		if d <= 0 {
			return fmt.Errorf("config: SWEEP_INTERVAL must be positive, got %q", v)
		}
		cfg.SweepInterval = d
	}

	cfg.UploadTTL = defaultUploadTTL
	if v := strings.TrimSpace(getenv("UPLOAD_TTL")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("config: invalid UPLOAD_TTL %q: %w", v, err)
		}
		if d <= 0 {
			return fmt.Errorf("config: UPLOAD_TTL must be positive, got %q", v)
		}
		cfg.UploadTTL = d
	}

	return nil
}

// loadWorker resolves the pipeline trigger and worker-stage settings. The
// trigger defaults to exec (spawns the local worker for `make demo`/`make dev`);
// deployments select cloudrun explicitly via WORKER_TRIGGER. The cloudrun job
// coordinates are validated only when that mode is selected, so dev and the
// existing prod smoke config boot with no extra worker env.
func loadWorker(cfg *Config, getenv func(string) string) error {
	if v := strings.TrimSpace(getenv("WORKER_TRIGGER")); v != "" {
		t := WorkerTrigger(v)
		switch t {
		case WorkerTriggerExec, WorkerTriggerCloudRun:
			cfg.WorkerTrigger = t
		default:
			return fmt.Errorf("config: invalid WORKER_TRIGGER %q (want exec|cloudrun)", v)
		}
	} else {
		cfg.WorkerTrigger = WorkerTriggerExec
	}

	cfg.WorkerBin = strings.TrimSpace(getenv("WORKER_BIN"))
	cfg.WorkerJobRegion = strings.TrimSpace(getenv("WORKER_JOB_REGION"))
	cfg.WorkerJobProject = strings.TrimSpace(getenv("WORKER_JOB_PROJECT"))
	cfg.WorkerJobName = strings.TrimSpace(getenv("WORKER_JOB_NAME"))
	if cfg.WorkerTrigger == WorkerTriggerCloudRun {
		if cfg.WorkerJobRegion == "" || cfg.WorkerJobProject == "" || cfg.WorkerJobName == "" {
			return fmt.Errorf("config: WORKER_JOB_REGION, WORKER_JOB_PROJECT, and WORKER_JOB_NAME are required when WORKER_TRIGGER=cloudrun")
		}
	}

	cfg.IngestTimeout = defaultIngestTimeout
	if v := strings.TrimSpace(getenv("INGEST_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("config: invalid INGEST_TIMEOUT %q: %w", v, err)
		}
		if d <= 0 {
			return fmt.Errorf("config: INGEST_TIMEOUT must be positive, got %q", v)
		}
		cfg.IngestTimeout = d
	}

	return nil
}

// loadBlob resolves and validates object-storage settings. Dev defaults to the
// offline local store; staging and prod must use gcs with an explicit bucket.
func loadBlob(cfg *Config, getenv func(string) string) error {
	isProdLike := cfg.Env == EnvStaging || cfg.Env == EnvProd

	if v := strings.TrimSpace(getenv("BLOB_MODE")); v != "" {
		m := BlobMode(v)
		switch m {
		case BlobModeLocal, BlobModeGCS:
			cfg.BlobMode = m
		default:
			return fmt.Errorf("config: invalid BLOB_MODE %q (want local|gcs)", v)
		}
	} else if isProdLike {
		cfg.BlobMode = BlobModeGCS
	} else {
		cfg.BlobMode = BlobModeLocal
	}
	if isProdLike && cfg.BlobMode == BlobModeLocal {
		return fmt.Errorf("config: BLOB_MODE=local is not allowed when ENV=%s", cfg.Env)
	}

	cfg.GCSBucket = strings.TrimSpace(getenv("GCS_BUCKET"))
	if cfg.BlobMode == BlobModeGCS && cfg.GCSBucket == "" {
		return fmt.Errorf("config: GCS_BUCKET is required when BLOB_MODE=gcs")
	}

	if v := strings.TrimSpace(getenv("BLOB_DIR")); v != "" {
		cfg.BlobDir = v
	} else if cfg.BlobMode == BlobModeLocal {
		cfg.BlobDir = filepath.Join(os.TempDir(), defaultBlobDirName)
	}

	return nil
}

// loadAuth resolves and validates the auth-related settings. The rules encode
// the deployment posture: dev is offline-friendly with safe defaults; staging
// and prod must be explicitly configured (real secret, identity mode, API key).
func loadAuth(cfg *Config, getenv func(string) string) error {
	isProdLike := cfg.Env == EnvStaging || cfg.Env == EnvProd

	// AUTH_MODE: default derives from env; explicit value is validated; dev
	// mode is refused outside dev.
	if v := strings.TrimSpace(getenv("AUTH_MODE")); v != "" {
		m := AuthMode(v)
		switch m {
		case AuthModeDev, AuthModeIdentity:
			cfg.AuthMode = m
		default:
			return fmt.Errorf("config: invalid AUTH_MODE %q (want dev|identity)", v)
		}
	} else if isProdLike {
		cfg.AuthMode = AuthModeIdentity
	} else {
		cfg.AuthMode = AuthModeDev
	}
	if isProdLike && cfg.AuthMode == AuthModeDev {
		return fmt.Errorf("config: AUTH_MODE=dev is not allowed when ENV=%s", cfg.Env)
	}

	// SESSION_SECRET: required outside dev; dev falls back to an insecure
	// default and flags it for a startup WARN.
	if v := strings.TrimSpace(getenv("SESSION_SECRET")); v != "" {
		cfg.SessionSecret = v
	} else if isProdLike {
		return fmt.Errorf("config: SESSION_SECRET is required when ENV=%s", cfg.Env)
	} else {
		cfg.SessionSecret = DevSessionSecret
		cfg.SessionSecretDefaulted = true
	}

	// DEV_PASSWORD: only meaningful in dev mode; always resolved so config is
	// self-describing.
	if v := getenv("DEV_PASSWORD"); v != "" {
		cfg.DevPassword = v
	} else {
		cfg.DevPassword = defaultDevPassword
	}

	// IDP_API_KEY: required in identity mode.
	cfg.IDPAPIKey = strings.TrimSpace(getenv("IDP_API_KEY"))
	if cfg.AuthMode == AuthModeIdentity && cfg.IDPAPIKey == "" {
		return fmt.Errorf("config: IDP_API_KEY is required when AUTH_MODE=identity")
	}

	return nil
}

func validPort(v string) bool {
	n, err := strconv.Atoi(v)
	return err == nil && n >= 1 && n <= 65535
}

func parseLevel(v string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("config: invalid LOG_LEVEL %q (want debug|info|warn|error)", v)
	}
}
