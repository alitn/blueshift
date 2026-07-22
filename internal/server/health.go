package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"sync"
)

// CheckFunc reports whether a dependency is ready. A nil error means ready.
type CheckFunc func(ctx context.Context) error

// Readiness is a pluggable registry of readiness checks. It is empty at startup
// in this milestone; later tasks (e.g. the DB baseline) register checks here.
type Readiness struct {
	mu     sync.RWMutex
	checks map[string]CheckFunc
}

// NewReadiness returns an empty readiness registry.
func NewReadiness() *Readiness {
	return &Readiness{checks: make(map[string]CheckFunc)}
}

// Register adds (or replaces) a named readiness check.
func (rd *Readiness) Register(name string, fn CheckFunc) {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	rd.checks[name] = fn
}

// snapshot returns a stable copy of the registered checks.
func (rd *Readiness) snapshot() map[string]CheckFunc {
	rd.mu.RLock()
	defer rd.mu.RUnlock()
	out := make(map[string]CheckFunc, len(rd.checks))
	for k, v := range rd.checks {
		out[k] = v
	}
	return out
}

type readyResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// healthz is liveness: always OK while the process is running.
func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyzHandler runs all registered checks. All pass -> 200 ready; any fail ->
// 503 listing the failing check names.
func (rd *Readiness) readyzHandler(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		checks := make(map[string]string)
		var failed []string

		for name, fn := range rd.snapshot() {
			if err := fn(r.Context()); err != nil {
				checks[name] = "failed"
				failed = append(failed, name)
				// Raw errors stay in server logs only (may name internals).
				logger.LogAttrs(r.Context(), slog.LevelError, "readiness check failed",
					slog.String("check", name),
					slog.String("error", err.Error()),
				)
			} else {
				checks[name] = "ok"
			}
		}

		if len(failed) > 0 {
			sort.Strings(failed)
			writeJSON(w, http.StatusServiceUnavailable, readyResponse{
				Status: "unavailable",
				Checks: checks,
			})
			return
		}
		writeJSON(w, http.StatusOK, readyResponse{Status: "ready", Checks: checks})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
