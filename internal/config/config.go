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

// ASRMode selects the speech-recognition backend the worker's transcribe stage
// registers. `fake` replays committed offline recordings (used by `make
// demo`/`make dev` and offline verification — deterministic, no credential or
// network); `speech` calls the managed provider-backed engine (staging/prod).
// The mode only chooses which engine backs the neutral label; nothing here or
// downstream of the /internal/asr seam names a provider.
type ASRMode string

const (
	ASRModeFake   ASRMode = "fake"
	ASRModeSpeech ASRMode = "speech"
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

	// ASRMode selects the speech-recognition backend (fake|speech). Defaults to
	// fake in dev and speech in staging/prod. Only the worker's transcribe stage
	// uses it; the app reads it solely so a worker it spawns inherits the value.
	ASRMode ASRMode
	// ASREngineLabel is the neutral label the ASR engine registers under and that
	// the transcribe stage resolves for a language's asr slot (default "bs-asr-1").
	// It never carries a provider name.
	ASREngineLabel string
	// ASRModel, ASRRegion, ASRProject, ASRBucket fully specify the provider-backed
	// engine (speech mode). They are read verbatim from the environment/deploy —
	// never defaulted here, so no provider/model string lives in this package — and
	// validated by the engine constructor, not at load, so the app boots without
	// them (only the worker's speech-mode wiring requires them).
	ASRModel   string
	ASRRegion  string
	ASRProject string
	ASRBucket  string
	// ASRLanguageCodes maps a BCP-47 content tag to the provider language code
	// (e.g. "fa" -> "fa-IR"), parsed from ASR_LANGUAGE_CODES ("fa=fa-IR,en=en-US").
	// Kept as data (env/deploy), so adding a language is a row, not a code change,
	// and no "fa" assumption lives in this package. Empty means every tag passes
	// through to the engine verbatim.
	ASRLanguageCodes map[string]string

	// PipelineStages is the ordered active stage chain the worker runs and
	// auto-advances through, from PIPELINE_STAGES (comma-separated). Empty (the
	// default) means the pipeline's default chain — ingest only, which makes ingest
	// terminal. This package only splits the list; the worker validates the names
	// against the stage registry at startup (an unknown stage, or a chain not
	// starting with ingest, fails fast), keeping the registry the single source of
	// stage truth. Transcribe (and later stages) join the active chain only when
	// named here — the reversible gate that keeps a worker without ASR config, and
	// the offline demo/e2e flow, ingest-terminal.
	PipelineStages []string

	// PipelineAutoAdvance controls whether a worker, on a non-terminal stage's
	// success, launches the next registered stage (via the same trigger the API
	// server uses). Maps to PIPELINE_AUTO_ADVANCE and defaults to true. When false
	// the completed stage's handoff is still recorded durably, but the next stage
	// is not launched — a staged-rollout / manual-drive mode. With only ingest
	// registered (M1) it has no observable effect: ingest is terminal.
	PipelineAutoAdvance bool

	// ProxyMaxRemuxBitrate is the overall-bitrate ceiling (bits/sec) under which an
	// already-browser-compatible master is remuxed (stream copy) into its proxy
	// rather than transcoded. Above it, the master is transcoded so a proxy always
	// streams cheaply. Defaults to ~6 Mbps.
	ProxyMaxRemuxBitrate int64

	// SweepInterval is the cadence of the abandoned-upload sweep (the app-side
	// TTL reaper). Defaults to 1h. The sweep only runs when a database is
	// configured (DATABASE_URL set).
	SweepInterval time.Duration
	// UploadTTL is how long a created-but-never-uploaded episode may sit at
	// 'uploaded' with no master key before the sweep removes it. Defaults to 6h.
	UploadTTL time.Duration
	// ProcessingTTL is how long an episode may sit at 'processing' (a live claim)
	// before the stale-claim sweep force-fails it — the backstop for a worker
	// killed mid-stage. Defaults to 5h (> the worker Job task-timeout plus slack)
	// so a legitimately long ingest is never failed out from under a live worker.
	ProcessingTTL time.Duration
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

	// defaultASREngineLabel is the neutral ASR engine label when ASR_ENGINE_LABEL
	// is unset. It names no provider (CLAUDE.md, "Vendor neutrality").
	defaultASREngineLabel = "bs-asr-1"

	// defaultProxyMaxRemuxBitrate is the remux bitrate ceiling (~6 Mbps, in
	// bits/sec) when PROXY_MAX_REMUX_BITRATE is unset. Mirrors
	// pipeline.defaultMaxRemuxBitrate.
	defaultProxyMaxRemuxBitrate = 6_000_000

	// defaultSweepInterval is the cadence of the abandoned-upload sweep when
	// SWEEP_INTERVAL is unset.
	defaultSweepInterval = time.Hour
	// defaultUploadTTL is how long an abandoned (created-but-never-uploaded)
	// episode may linger before the sweep removes it, when UPLOAD_TTL is unset.
	defaultUploadTTL = 6 * time.Hour
	// defaultProcessingTTL is how long a 'processing' claim may age before the
	// stale-claim sweep force-fails it, when PROCESSING_TTL is unset. It is longer
	// than the worker Job's task-timeout (4h) plus slack so only a genuinely dead
	// claim is reaped, never a live long-running ingest.
	defaultProcessingTTL = 5 * time.Hour
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

	if err := loadASR(&cfg, getenv); err != nil {
		return Config{}, err
	}

	if err := loadSweep(&cfg, getenv); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// loadASR resolves the speech-recognition wiring. The mode defaults to fake in
// dev and speech in staging/prod (mirroring blob/auth), and fake is refused
// outside dev so a production worker never silently replays fixtures. The
// provider coordinates (model/region/project/bucket) are read verbatim — never
// defaulted here, so no provider/model string appears in this package — and their
// requiredness is enforced by the engine constructor at wiring time, not at load:
// the API server (which never builds an engine) must boot even when they are
// unset. The language-code map is parsed from ASR_LANGUAGE_CODES ("fa=fa-IR,...").
func loadASR(cfg *Config, getenv func(string) string) error {
	isProdLike := cfg.Env == EnvStaging || cfg.Env == EnvProd

	if v := strings.TrimSpace(getenv("ASR_ENGINE_MODE")); v != "" {
		m := ASRMode(v)
		switch m {
		case ASRModeFake, ASRModeSpeech:
			cfg.ASRMode = m
		default:
			return fmt.Errorf("config: invalid ASR_ENGINE_MODE %q (want fake|speech)", v)
		}
	} else if isProdLike {
		cfg.ASRMode = ASRModeSpeech
	} else {
		cfg.ASRMode = ASRModeFake
	}
	if isProdLike && cfg.ASRMode == ASRModeFake {
		return fmt.Errorf("config: ASR_ENGINE_MODE=fake is not allowed when ENV=%s", cfg.Env)
	}

	cfg.ASREngineLabel = defaultASREngineLabel
	if v := strings.TrimSpace(getenv("ASR_ENGINE_LABEL")); v != "" {
		cfg.ASREngineLabel = v
	}

	cfg.ASRModel = strings.TrimSpace(getenv("ASR_MODEL"))
	cfg.ASRRegion = strings.TrimSpace(getenv("ASR_REGION"))
	cfg.ASRProject = strings.TrimSpace(getenv("ASR_PROJECT"))
	cfg.ASRBucket = strings.TrimSpace(getenv("ASR_BUCKET"))

	codes, err := parseLanguageCodes(getenv("ASR_LANGUAGE_CODES"))
	if err != nil {
		return err
	}
	cfg.ASRLanguageCodes = codes

	return nil
}

// parseLanguageCodes parses a "tag=code,tag=code" list into a map. Whitespace
// around entries and around each side is trimmed; an empty input is an empty map.
// A malformed entry (no '=', or an empty tag/code) is a hard error so a
// misconfigured mapping fails fast rather than silently dropping a language.
func parseLanguageCodes(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]string{}, nil
	}
	out := map[string]string{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		tag, code, ok := strings.Cut(entry, "=")
		tag, code = strings.TrimSpace(tag), strings.TrimSpace(code)
		if !ok || tag == "" || code == "" {
			return nil, fmt.Errorf("config: invalid ASR_LANGUAGE_CODES entry %q (want tag=code)", entry)
		}
		out[tag] = code
	}
	return out, nil
}

// loadSweep resolves the sweep cadence and the two TTLs it enforces: the
// abandoned-upload TTL and the stale-'processing'-claim TTL. All default to
// production-safe values (1h cadence, 6h upload TTL, 5h processing TTL) and
// accept any positive Go duration (e.g. "2s", "90m") so a transient env can
// drive a fast sweep for verification. The sweep itself is wired only when a
// database is configured.
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

	cfg.ProcessingTTL = defaultProcessingTTL
	if v := strings.TrimSpace(getenv("PROCESSING_TTL")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("config: invalid PROCESSING_TTL %q: %w", v, err)
		}
		if d <= 0 {
			return fmt.Errorf("config: PROCESSING_TTL must be positive, got %q", v)
		}
		cfg.ProcessingTTL = d
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

	// PIPELINE_STAGES is an ordered, comma-separated active stage chain. Unset (or
	// all-empty) means the pipeline default (ingest only); the worker validates the
	// names against the stage registry at startup.
	if v := strings.TrimSpace(getenv("PIPELINE_STAGES")); v != "" {
		for _, p := range strings.Split(v, ",") {
			if s := strings.TrimSpace(p); s != "" {
				cfg.PipelineStages = append(cfg.PipelineStages, s)
			}
		}
	}

	// PIPELINE_AUTO_ADVANCE defaults to true; an explicit value must be a boolean.
	cfg.PipelineAutoAdvance = true
	if v := strings.TrimSpace(getenv("PIPELINE_AUTO_ADVANCE")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("config: invalid PIPELINE_AUTO_ADVANCE %q (want a boolean): %w", v, err)
		}
		cfg.PipelineAutoAdvance = b
	}

	cfg.ProxyMaxRemuxBitrate = defaultProxyMaxRemuxBitrate
	if v := strings.TrimSpace(getenv("PROXY_MAX_REMUX_BITRATE")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("config: invalid PROXY_MAX_REMUX_BITRATE %q (want an integer bits/sec): %w", v, err)
		}
		if n <= 0 {
			return fmt.Errorf("config: PROXY_MAX_REMUX_BITRATE must be positive, got %q", v)
		}
		cfg.ProxyMaxRemuxBitrate = n
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
