// Command app is the Blueshift API server with the embedded web build. It is
// intentionally thin: wiring only. All logic lives under internal/.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"blueshift/internal/api"
	"blueshift/internal/auth"
	"blueshift/internal/config"
	"blueshift/internal/logx"
	"blueshift/internal/server"
	"blueshift/internal/store"
	"blueshift/internal/webembed"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := logx.New(cfg.LogLevel, os.Stdout)
	logger.Info("starting", "env", string(cfg.Env), "port", cfg.Port)

	ui, err := webembed.Handler()
	if err != nil {
		return fmt.Errorf("web embed: %w", err)
	}

	ready := server.NewReadiness()

	// The database is optional in this milestone: only when DATABASE_URL is set
	// do we open a pool and register the "db" readiness check. Without it the
	// app still boots and serves /healthz and the UI; login then reports the
	// backend cleanly as unavailable.
	var st *store.Store
	if cfg.DatabaseURL != "" {
		st, err = store.Open(context.Background(), cfg.DatabaseURL)
		if err != nil {
			return fmt.Errorf("store: %w", err)
		}
		defer st.Close()
		ready.Register("db", st.Ping)
		logger.Info("database configured", "readyz_check", "db")
	} else {
		logger.Info("no DATABASE_URL set; database readiness check disabled")
	}

	apiHandler := buildAPI(cfg, logger, st)
	srv := server.New(cfg, logger, ui, ready, apiHandler)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return server.Run(ctx, srv, logger)
}

// buildAPI wires the auth surface: session codec, login backend for the
// configured mode, and the deny-by-default gate around the /api routes. st may
// be nil (no database configured); the directory then reports the backend
// unavailable rather than crashing.
func buildAPI(cfg config.Config, logger *slog.Logger, st *store.Store) http.Handler {
	codec := auth.NewCodec(cfg.SessionSecret)
	cookie := auth.CookieConfig{Secure: cfg.Env != config.EnvDev}
	if cfg.SessionSecretDefaulted {
		logger.Warn("SESSION_SECRET not set; using an insecure dev default (dev only)")
	}

	var q auth.AuthQuerier
	if st != nil {
		q = st
	}
	dir := auth.NewStoreDirectory(q)

	var authenticator auth.Authenticator
	switch cfg.AuthMode {
	case config.AuthModeIdentity:
		authenticator = auth.IdentityAuthenticator{
			APIKey: cfg.IDPAPIKey,
			Dir:    dir,
			Client: &http.Client{Timeout: 10 * time.Second},
		}
		logger.Info("auth mode: identity")
	default:
		authenticator = auth.DevAuthenticator{Password: cfg.DevPassword, Dir: dir}
		logger.Info("auth mode: dev (offline password sign-in)")
	}

	router := api.NewRouter(api.Deps{
		Authenticator: authenticator,
		Directory:     dir,
		Codec:         codec,
		Cookie:        cookie,
		Logger:        logger,
	})
	return server.AuthGate(codec, logger, router)
}
