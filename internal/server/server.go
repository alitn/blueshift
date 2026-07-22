// Package server wires the app's HTTP surface: health/readiness endpoints, the
// embedded UI, structured request logging, panic recovery, and graceful
// shutdown. main.go holds no logic beyond calling into here.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"blueshift/internal/config"
)

const (
	readTimeout       = 15 * time.Second
	readHeaderTimeout = 10 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 10 * time.Second
)

// New builds the HTTP server: routes, middleware, and explicit timeouts. The
// readiness registry is supplied by the caller so later tasks can register
// dependency checks (DB, storage) without changing this signature. api is the
// (already gated) /api handler; it may be nil in tests that exercise only the
// public surface. /healthz, /readyz, and the SPA stay public.
func New(cfg config.Config, logger *slog.Logger, ui http.Handler, ready *Readiness, api http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.Handle("GET /readyz", ready.readyzHandler(logger))
	if api != nil {
		mux.Handle("/api/", api)
	}
	mux.Handle("/", ui)

	// requestLogger is outermost so panics recovered below are still logged
	// with their final 500 status.
	handler := chain(mux, requestLogger(logger), recoverer(logger))

	return &http.Server{
		Addr:              cfg.Addr(),
		Handler:           handler,
		ReadTimeout:       readTimeout,
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
}

// Run binds srv.Addr and serves until ctx is cancelled, then shuts down
// gracefully. It is the entrypoint main uses.
func Run(ctx context.Context, srv *http.Server, logger *slog.Logger) error {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", srv.Addr, err)
	}
	return Serve(ctx, srv, ln, logger)
}

// Serve runs srv on the given listener until ctx is cancelled, then drains
// in-flight requests within shutdownTimeout. It returns nil on a clean
// shutdown. Splitting Run/Serve lets tests drive a real port deterministically.
func Serve(ctx context.Context, srv *http.Server, ln net.Listener, logger *slog.Logger) error {
	serveErr := make(chan error, 1)
	go func() {
		logger.Info("server started", "addr", ln.Addr().String())
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		// Serve returned before any shutdown signal: a real bind/serve error.
		return err
	case <-ctx.Done():
		logger.Info("shutdown initiated")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err.Error())
		return err
	}
	if err := <-serveErr; err != nil {
		return err
	}
	logger.Info("server stopped")
	return nil
}
