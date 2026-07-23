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
	"blueshift/internal/blob"
	"blueshift/internal/config"
	"blueshift/internal/logx"
	"blueshift/internal/pipeline"
	"blueshift/internal/server"
	"blueshift/internal/store"
	"blueshift/internal/sweep"
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

	bs, err := buildBlob(context.Background(), cfg)
	if err != nil {
		return fmt.Errorf("blob: %w", err)
	}

	apiHandler := buildAPI(cfg, logger, st, bs)
	srv := server.New(cfg, logger, ui, ready, apiHandler)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The abandoned-upload sweep is a system-level TTL reaper: a create can
	// succeed server-side and then the client abandons the upload, leaving a row
	// stuck at 'uploaded' with no master key. It runs only when a database is
	// configured (st != nil) and is bound to the app's lifecycle ctx, so it drains
	// cleanly on shutdown.
	if st != nil {
		go sweep.Run(ctx, st, logger, cfg.SweepInterval, cfg.UploadTTL)
		logger.Info("abandoned-upload sweep enabled",
			"interval", cfg.SweepInterval.String(), "ttl", cfg.UploadTTL.String())
	}

	return server.Run(ctx, srv, logger)
}

// buildBlob constructs the object-storage backend for the configured mode. The
// local store signs its upload tokens with the session secret, reusing the same
// secret machinery as the auth cookies.
func buildBlob(ctx context.Context, cfg config.Config) (blob.Store, error) {
	switch cfg.BlobMode {
	case config.BlobModeGCS:
		return blob.NewGCS(ctx, cfg.GCSBucket)
	default:
		return blob.NewLocal(cfg.BlobDir, []byte(cfg.SessionSecret), nil)
	}
}

// buildTrigger constructs the pipeline worker trigger for the configured mode.
// exec spawns the local worker binary (dev/demo); cloudrun starts a Cloud Run
// Jobs execution. Both satisfy api.StageTrigger; the provider detail stays
// inside internal/pipeline.
func buildTrigger(cfg config.Config, logger *slog.Logger) api.StageTrigger {
	switch cfg.WorkerTrigger {
	case config.WorkerTriggerCloudRun:
		logger.Info("worker trigger: cloudrun", "job", cfg.WorkerJobName, "region", cfg.WorkerJobRegion)
		return pipeline.NewCloudRunTrigger(cfg.WorkerJobProject, cfg.WorkerJobRegion, cfg.WorkerJobName, logger)
	default:
		logger.Info("worker trigger: exec", "bin", cfg.WorkerBin)
		return pipeline.NewExecTrigger(cfg.WorkerBin, logger)
	}
}

// buildAPI wires the auth surface: session codec, login backend for the
// configured mode, and the deny-by-default gate around the /api routes. In local
// blob mode it also mounts the token-authenticated upload endpoint ahead of the
// gate. st may be nil (no database configured); the directory then reports the
// backend unavailable rather than crashing, and the episode routes stay off.
func buildAPI(cfg config.Config, logger *slog.Logger, st *store.Store, bs blob.Store) http.Handler {
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

	deps := api.Deps{
		Authenticator: authenticator,
		Directory:     dir,
		Codec:         codec,
		Cookie:        cookie,
		Logger:        logger,
		Blob:          bs,
		PublicBaseURL: cfg.PublicBaseURL,
	}
	// Episode routes need both the store and the blob backend; without a
	// database they stay off (the rest of /api still serves).
	if st != nil {
		deps.Episodes = st
		deps.Trigger = buildTrigger(cfg, logger)
	}
	router := api.NewRouter(deps)
	gated := server.AuthGate(codec, logger, router)

	// In local blob mode the upload endpoint authenticates by signed token, not
	// the session cookie, so it is mounted ahead of the gate. Its path is more
	// specific than "/api/", so it wins for upload requests.
	if local, ok := bs.(*blob.Local); ok {
		mux := http.NewServeMux()
		mux.Handle(blob.LocalBasePath+"/", local.Handler())
		mux.Handle("/api/", gated)
		return mux
	}
	return gated
}
