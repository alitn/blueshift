// Package config loads the app server's runtime configuration from the
// environment. Secret Manager values are injected as env vars by Cloud Run
// (--set-secrets); there is no Secret Manager client here.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
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
}

// Addr returns the listen address (":<port>") for http.Server.
func (c Config) Addr() string { return ":" + c.Port }

// Default values applied when the corresponding env var is unset or empty.
const (
	defaultPort     = "8080"
	defaultEnv      = EnvDev
	defaultLogLevel = slog.LevelInfo
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

	return cfg, nil
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
