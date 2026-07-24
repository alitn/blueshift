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

func TestPublicBaseURL(t *testing.T) {
	// Unset by default.
	cfg, err := load(env(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PublicBaseURL != "" {
		t.Errorf("PublicBaseURL = %q, want empty when unset", cfg.PublicBaseURL)
	}
	// Explicit value is trimmed and carried through.
	cfg, err = load(env(map[string]string{"PUBLIC_BASE_URL": "  https://app.example.com  "}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PublicBaseURL != "https://app.example.com" {
		t.Errorf("PublicBaseURL = %q, want the trimmed URL", cfg.PublicBaseURL)
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
	if cfg.ProxyMaxRemuxBitrate != defaultProxyMaxRemuxBitrate {
		t.Errorf("ProxyMaxRemuxBitrate = %d, want %d", cfg.ProxyMaxRemuxBitrate, defaultProxyMaxRemuxBitrate)
	}
	if cfg.MaxProcessAttempts != defaultMaxProcessAttempts {
		t.Errorf("MaxProcessAttempts = %d, want %d", cfg.MaxProcessAttempts, defaultMaxProcessAttempts)
	}
	if cfg.Reprocess {
		t.Error("Reprocess default = true, want false (a plain run must never re-bill)")
	}
	// Resegmentation knobs default to zero — "defer to the code defaults in
	// internal/asr" — so the defaults live in exactly one place.
	if cfg.SegmentGapMs != 0 || cfg.SegmentMaxDurationMs != 0 || cfg.SegmentMaxWords != 0 {
		t.Errorf("segmentation knobs = %d/%d/%d, want 0/0/0 (unset defers to code defaults)",
			cfg.SegmentGapMs, cfg.SegmentMaxDurationMs, cfg.SegmentMaxWords)
	}
}

// TestLoadSegmentationKnobs covers the transcribe stage's resegmentation
// thresholds: explicit positive values flow through verbatim.
func TestLoadSegmentationKnobs(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"SEGMENT_GAP_MS":          "550",
		"SEGMENT_MAX_DURATION_MS": "20000",
		"SEGMENT_MAX_WORDS":       "40",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SegmentGapMs != 550 {
		t.Errorf("SegmentGapMs = %d, want 550", cfg.SegmentGapMs)
	}
	if cfg.SegmentMaxDurationMs != 20_000 {
		t.Errorf("SegmentMaxDurationMs = %d, want 20000", cfg.SegmentMaxDurationMs)
	}
	if cfg.SegmentMaxWords != 40 {
		t.Errorf("SegmentMaxWords = %d, want 40", cfg.SegmentMaxWords)
	}
}

// TestLoadCostSafety covers the cost-safety worker knobs: the per-episode
// billable-attempt cap and the deliberate reprocess override.
func TestLoadCostSafety(t *testing.T) {
	cfg, err := load(env(map[string]string{"MAX_PROCESS_ATTEMPTS": "12", "PIPELINE_REPROCESS": "true"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.MaxProcessAttempts != 12 {
		t.Errorf("MaxProcessAttempts = %d, want 12", cfg.MaxProcessAttempts)
	}
	if !cfg.Reprocess {
		t.Error("PIPELINE_REPROCESS=true did not set Reprocess")
	}
}

func TestLoadProxyMaxRemuxBitrate(t *testing.T) {
	cfg, err := load(env(map[string]string{"PROXY_MAX_REMUX_BITRATE": "8000000"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ProxyMaxRemuxBitrate != 8_000_000 {
		t.Errorf("ProxyMaxRemuxBitrate = %d, want 8000000", cfg.ProxyMaxRemuxBitrate)
	}
}

func TestLoadPipelineAutoAdvance(t *testing.T) {
	// Default: on.
	cfg, err := load(env(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.PipelineAutoAdvance {
		t.Error("PipelineAutoAdvance default = false, want true")
	}
	// Explicit off.
	cfg, err = load(env(map[string]string{"PIPELINE_AUTO_ADVANCE": "false"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PipelineAutoAdvance {
		t.Error("PIPELINE_AUTO_ADVANCE=false did not disable auto-advance")
	}
	// Explicit on (accepts the usual boolean spellings).
	cfg, err = load(env(map[string]string{"PIPELINE_AUTO_ADVANCE": "1"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.PipelineAutoAdvance {
		t.Error("PIPELINE_AUTO_ADVANCE=1 did not enable auto-advance")
	}
	// Invalid -> error.
	if _, err := load(env(map[string]string{"PIPELINE_AUTO_ADVANCE": "maybe"})); err == nil {
		t.Error("PIPELINE_AUTO_ADVANCE=maybe: expected error, got nil")
	}
}

func TestLoadPipelineStages(t *testing.T) {
	// Default: unset -> empty, meaning the pipeline's default (ingest-only) chain.
	cfg, err := load(env(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.PipelineStages) != 0 {
		t.Errorf("PipelineStages default = %v, want empty (pipeline default)", cfg.PipelineStages)
	}
	// Comma-separated, trimmed, empty tokens dropped.
	cfg, err = load(env(map[string]string{"PIPELINE_STAGES": " ingest , transcribe ,"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.PipelineStages; len(got) != 2 || got[0] != "ingest" || got[1] != "transcribe" {
		t.Errorf("PipelineStages = %v, want [ingest transcribe]", got)
	}
	// All-empty tokens -> empty (pipeline default). Config only splits; the worker
	// validates names against the stage registry at startup.
	cfg, err = load(env(map[string]string{"PIPELINE_STAGES": " , ,"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.PipelineStages) != 0 {
		t.Errorf("PipelineStages = %v, want empty for an all-empty list", cfg.PipelineStages)
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
		"invalid trigger":           {"WORKER_TRIGGER": "lambda"},
		"cloudrun missing coords":   {"WORKER_TRIGGER": "cloudrun"},
		"cloudrun partial coords":   {"WORKER_TRIGGER": "cloudrun", "WORKER_JOB_REGION": "r"},
		"bad ingest timeout":        {"INGEST_TIMEOUT": "soon"},
		"nonpositive ingest tmout":  {"INGEST_TIMEOUT": "0s"},
		"bad remux bitrate":         {"PROXY_MAX_REMUX_BITRATE": "lots"},
		"nonpositive remux bitrate": {"PROXY_MAX_REMUX_BITRATE": "0"},
		"negative remux bitrate":    {"PROXY_MAX_REMUX_BITRATE": "-1"},
		"bad max attempts":          {"MAX_PROCESS_ATTEMPTS": "many"},
		"nonpositive max attempts":  {"MAX_PROCESS_ATTEMPTS": "0"},
		"negative max attempts":     {"MAX_PROCESS_ATTEMPTS": "-1"},
		"bad reprocess":             {"PIPELINE_REPROCESS": "maybe"},
		"bad segment gap":           {"SEGMENT_GAP_MS": "long"},
		"nonpositive segment gap":   {"SEGMENT_GAP_MS": "0"},
		"negative segment gap":      {"SEGMENT_GAP_MS": "-700"},
		"bad segment duration":      {"SEGMENT_MAX_DURATION_MS": "half a minute"},
		"nonpositive segment dur":   {"SEGMENT_MAX_DURATION_MS": "0"},
		"bad segment words":         {"SEGMENT_MAX_WORDS": "sixty"},
		"nonpositive segment words": {"SEGMENT_MAX_WORDS": "0"},
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
	if cfg.ProcessingTTL != defaultProcessingTTL {
		t.Errorf("ProcessingTTL = %v, want %v", cfg.ProcessingTTL, defaultProcessingTTL)
	}
}

func TestLoadSweepOverrides(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"SWEEP_INTERVAL": "2s",
		"UPLOAD_TTL":     "1s",
		"PROCESSING_TTL": "3s",
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
	if cfg.ProcessingTTL != 3*time.Second {
		t.Errorf("ProcessingTTL = %v, want 3s", cfg.ProcessingTTL)
	}
}

func TestLoadSweepInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"bad sweep interval":         {"SWEEP_INTERVAL": "often"},
		"nonpositive interval":       {"SWEEP_INTERVAL": "0s"},
		"negative interval":          {"SWEEP_INTERVAL": "-1h"},
		"bad upload ttl":             {"UPLOAD_TTL": "forever"},
		"nonpositive upload ttl":     {"UPLOAD_TTL": "0s"},
		"bad processing ttl":         {"PROCESSING_TTL": "eventually"},
		"nonpositive processing ttl": {"PROCESSING_TTL": "0s"},
		"negative processing ttl":    {"PROCESSING_TTL": "-2h"},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := load(env(m)); err == nil {
				t.Fatalf("load(%v): expected error, got nil", m)
			}
		})
	}
}

func TestLoadASRDefaults(t *testing.T) {
	// Dev defaults to the offline fake engine under the neutral label; no provider
	// coordinates are required and the language-code map is empty.
	cfg, err := load(env(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ASRMode != ASRModeFake {
		t.Errorf("ASRMode = %q, want fake in dev", cfg.ASRMode)
	}
	if cfg.ASREngineLabel != defaultASREngineLabel {
		t.Errorf("ASREngineLabel = %q, want %q", cfg.ASREngineLabel, defaultASREngineLabel)
	}
	// The public speech label is PINNED at bs-asr-2 (bumped from bs-asr-1 for
	// the 2026-07-24 engine-behaviour change): a drift here silently mislabels
	// every new stage-run provenance row, so the literal is asserted, not just
	// the constant.
	if cfg.ASREngineLabel != "bs-asr-2" {
		t.Errorf("ASREngineLabel default = %q, want the versioned public label bs-asr-2", cfg.ASREngineLabel)
	}
	if len(cfg.ASRLanguageCodes) != 0 {
		t.Errorf("ASRLanguageCodes = %v, want empty", cfg.ASRLanguageCodes)
	}
	// Provenance knobs: no duration rate by default (no cost recorded), and the
	// ingest provenance label defaults to the neutral bs-media-1.
	if cfg.ASRPriceCentsPerHour != 0 {
		t.Errorf("ASRPriceCentsPerHour = %d, want 0 (unset records no cost)", cfg.ASRPriceCentsPerHour)
	}
	if cfg.MediaEngineLabel != "bs-media-1" {
		t.Errorf("MediaEngineLabel = %q, want bs-media-1", cfg.MediaEngineLabel)
	}
}

func TestLoadASRPriceAndMediaLabelOverrides(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"ASR_PRICE_CENTS_PER_HOUR": "96",
		"MEDIA_ENGINE_LABEL":       "bs-media-2",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ASRPriceCentsPerHour != 96 {
		t.Errorf("ASRPriceCentsPerHour = %d, want 96", cfg.ASRPriceCentsPerHour)
	}
	if cfg.MediaEngineLabel != "bs-media-2" {
		t.Errorf("MediaEngineLabel = %q, want bs-media-2", cfg.MediaEngineLabel)
	}
	// A malformed rate is a startup error, never a silent zero.
	if _, err := load(env(map[string]string{"ASR_PRICE_CENTS_PER_HOUR": "-3"})); err == nil {
		t.Error("negative ASR_PRICE_CENTS_PER_HOUR: want error, got nil")
	}
}

func TestLoadASRProdDefaultsSpeech(t *testing.T) {
	// Prod derives speech mode and must still boot with the ASR coordinates unset:
	// requiredness is enforced by the engine constructor at wiring time, not here,
	// so the API server (which never builds an engine) is never blocked on them.
	cfg, err := load(env(map[string]string{
		"ENV": "prod", "SESSION_SECRET": "s", "IDP_API_KEY": "k", "GCS_BUCKET": "b",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ASRMode != ASRModeSpeech {
		t.Errorf("ASRMode = %q, want speech in prod", cfg.ASRMode)
	}
}

func TestLoadASRSpeechOverrides(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"ASR_ENGINE_MODE":    "speech",
		"ASR_ENGINE_LABEL":   "bs-asr-2",
		"ASR_MODEL":          "some-model",
		"ASR_REGION":         "us-central1",
		"ASR_PROJECT":        "bs-proj",
		"ASR_BUCKET":         "bs-media",
		"ASR_LANGUAGE_CODES": " fa=fa-IR , en = en-US ",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ASRMode != ASRModeSpeech {
		t.Errorf("ASRMode = %q, want speech", cfg.ASRMode)
	}
	if cfg.ASREngineLabel != "bs-asr-2" {
		t.Errorf("ASREngineLabel = %q, want bs-asr-2", cfg.ASREngineLabel)
	}
	if cfg.ASRModel != "some-model" || cfg.ASRRegion != "us-central1" || cfg.ASRProject != "bs-proj" || cfg.ASRBucket != "bs-media" {
		t.Errorf("speech coords = %q/%q/%q/%q", cfg.ASRModel, cfg.ASRRegion, cfg.ASRProject, cfg.ASRBucket)
	}
	if cfg.ASRLanguageCodes["fa"] != "fa-IR" || cfg.ASRLanguageCodes["en"] != "en-US" {
		t.Errorf("ASRLanguageCodes = %v, want fa->fa-IR, en->en-US (trimmed)", cfg.ASRLanguageCodes)
	}
}

func TestLoadASRInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"invalid mode":         {"ASR_ENGINE_MODE": "magic"},
		"fake in prod":         {"ENV": "prod", "SESSION_SECRET": "s", "IDP_API_KEY": "k", "GCS_BUCKET": "b", "ASR_ENGINE_MODE": "fake"},
		"lang codes no equals": {"ASR_LANGUAGE_CODES": "fa"},
		"lang codes empty tag": {"ASR_LANGUAGE_CODES": "=fa-IR"},
		"lang codes empty val": {"ASR_LANGUAGE_CODES": "fa="},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := load(env(m)); err == nil {
				t.Fatalf("load(%v): expected error, got nil", m)
			}
		})
	}
}

func TestLoadLLMDefaults(t *testing.T) {
	// Dev defaults to the offline fake engine under the neutral label; no provider
	// coordinates are required and no price is configured.
	cfg, err := load(env(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LLMMode != LLMModeFake {
		t.Errorf("LLMMode = %q, want fake in dev", cfg.LLMMode)
	}
	if cfg.LLMEngineLabel != defaultLLMEngineLabel {
		t.Errorf("LLMEngineLabel = %q, want %q", cfg.LLMEngineLabel, defaultLLMEngineLabel)
	}
	if cfg.LLMPriceInCentsPerMTok != 0 || cfg.LLMPriceOutCentsPerMTok != 0 {
		t.Errorf("price = %d/%d, want unset (0/0)", cfg.LLMPriceInCentsPerMTok, cfg.LLMPriceOutCentsPerMTok)
	}
}

func TestLoadLLMProdDefaultsLive(t *testing.T) {
	// Prod derives live mode and must still boot with the LLM coordinates unset:
	// requiredness is enforced by the /internal/llm constructor at wiring time,
	// not here, so the API server (which never builds an LLM client) is never
	// blocked on them.
	cfg, err := load(env(map[string]string{
		"ENV": "prod", "SESSION_SECRET": "s", "IDP_API_KEY": "k", "GCS_BUCKET": "b",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LLMMode != LLMModeLive {
		t.Errorf("LLMMode = %q, want live in prod", cfg.LLMMode)
	}
}

func TestLoadLLMLiveOverrides(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"LLM_ENGINE_MODE":              "live",
		"LLM_ENGINE_LABEL":             "bs-lm-2",
		"LLM_PROVIDER":                 "some-provider",
		"LLM_MODEL":                    "some-model",
		"LLM_ENDPOINT":                 "https://models.example.test/v1/models",
		"LLM_PROJECT":                  "bs-proj",
		"LLM_REGION":                   "some-region",
		"LLM_API_KEY":                  "k",
		"LLM_PRICE_IN_CENTS_PER_MTOK":  "150",
		"LLM_PRICE_OUT_CENTS_PER_MTOK": "900",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LLMMode != LLMModeLive {
		t.Errorf("LLMMode = %q, want live", cfg.LLMMode)
	}
	if cfg.LLMEngineLabel != "bs-lm-2" {
		t.Errorf("LLMEngineLabel = %q, want bs-lm-2", cfg.LLMEngineLabel)
	}
	if cfg.LLMProvider != "some-provider" || cfg.LLMModel != "some-model" ||
		cfg.LLMEndpoint != "https://models.example.test/v1/models" ||
		cfg.LLMProject != "bs-proj" || cfg.LLMRegion != "some-region" || cfg.LLMAPIKey != "k" {
		t.Errorf("live coords = %q/%q/%q/%q/%q (key set: %t)",
			cfg.LLMProvider, cfg.LLMModel, cfg.LLMEndpoint, cfg.LLMProject, cfg.LLMRegion, cfg.LLMAPIKey != "")
	}
	if cfg.LLMPriceInCentsPerMTok != 150 || cfg.LLMPriceOutCentsPerMTok != 900 {
		t.Errorf("price = %d/%d, want 150/900", cfg.LLMPriceInCentsPerMTok, cfg.LLMPriceOutCentsPerMTok)
	}
}

func TestLoadLLMInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"invalid mode":       {"LLM_ENGINE_MODE": "magic"},
		"fake in prod":       {"ENV": "prod", "SESSION_SECRET": "s", "IDP_API_KEY": "k", "GCS_BUCKET": "b", "LLM_ENGINE_MODE": "fake"},
		"price not a number": {"LLM_PRICE_IN_CENTS_PER_MTOK": "cheap", "LLM_PRICE_OUT_CENTS_PER_MTOK": "900"},
		"price nonpositive":  {"LLM_PRICE_IN_CENTS_PER_MTOK": "0", "LLM_PRICE_OUT_CENTS_PER_MTOK": "900"},
		"price input only":   {"LLM_PRICE_IN_CENTS_PER_MTOK": "150"},
		"price output only":  {"LLM_PRICE_OUT_CENTS_PER_MTOK": "900"},
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
