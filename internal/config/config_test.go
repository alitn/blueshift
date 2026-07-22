package config

import (
	"log/slog"
	"testing"
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
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"PORT":      "9090",
		"ENV":       "prod",
		"LOG_LEVEL": "warning",
	}))
	if err != nil {
		t.Fatalf("load: unexpected error: %v", err)
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
