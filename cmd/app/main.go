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
	"blueshift/internal/llm"
	"blueshift/internal/logx"
	"blueshift/internal/moments"
	"blueshift/internal/pipeline"
	"blueshift/internal/server"
	"blueshift/internal/store"
	"blueshift/internal/sweep"
	"blueshift/internal/webembed"

	// Register the supported content languages with the /internal/lang registry
	// (import for side effect): the compose seam resolves an episode's language
	// through the registry exactly like the worker's LLM stages do.
	_ "blueshift/internal/lang/fa"
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

	apiHandler, err := buildAPI(cfg, logger, st, bs)
	if err != nil {
		return fmt.Errorf("api: %w", err)
	}
	srv := server.New(cfg, logger, ui, ready, apiHandler)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The maintenance sweep is a system-level reaper with two jobs: reap abandoned
	// uploads (a create that succeeded server-side then the client abandoned the
	// upload, leaving a row at 'uploaded' with no master key), and force-fail
	// stale 'processing' claims (a worker killed mid-stage, which would otherwise
	// wedge the episode forever). It runs only when a database is configured
	// (st != nil) and is bound to the app's lifecycle ctx, so it drains cleanly on
	// shutdown.
	if st != nil {
		go sweep.Run(ctx, st, logger, cfg.SweepInterval, cfg.UploadTTL, cfg.ProcessingTTL)
		logger.Info("maintenance sweep enabled",
			"interval", cfg.SweepInterval.String(),
			"upload_ttl", cfg.UploadTTL.String(),
			"processing_ttl", cfg.ProcessingTTL.String())
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
func buildAPI(cfg config.Config, logger *slog.Logger, st *store.Store, bs blob.Store) (http.Handler, error) {
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
		// The free-prompt compose seam: the same audited LLM client shape the
		// worker's LLM stages use, replaying the committed compose recording in
		// fake mode (dev/demo/CI — offline, free) and binding the provider env in
		// live mode. A misconfigured live engine fails fast here at boot.
		composer, err := buildComposer(cfg, st, logger)
		if err != nil {
			return nil, err
		}
		deps.Composer = composer
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
		return mux, nil
	}
	return gated, nil
}

// buildComposer constructs the free-prompt moment composition seam: an
// audited, schema-validated llm.Client (fake mode replays the committed
// compose recording through the real validate/retry loop; live mode binds the
// LLM_* provider env to the neutral label, mirroring the worker's
// buildLLMClient) joined to the org-scoped store reads/writes. Provider names
// stay confined to /internal/llm and the deploy env.
func buildComposer(cfg config.Config, st *store.Store, logger *slog.Logger) (api.MomentComposer, error) {
	auditor := llm.NewDBAuditor(st)
	var (
		client *llm.Client
		err    error
	)
	switch cfg.LLMMode {
	case config.LLMModeLive:
		var price *llm.Price
		if cfg.LLMPriceInCentsPerMTok > 0 && cfg.LLMPriceOutCentsPerMTok > 0 {
			price = &llm.Price{
				InputPerMTokCents:  cfg.LLMPriceInCentsPerMTok,
				OutputPerMTokCents: cfg.LLMPriceOutCentsPerMTok,
			}
		}
		client, err = llm.New(llm.Options{
			Engines: []llm.EngineConfig{{
				Label:    cfg.LLMEngineLabel,
				Provider: cfg.LLMProvider,
				Model:    cfg.LLMModel,
				Price:    price,
			}},
			Auditor: auditor,
			Logger:  logger,
			Gemini: llm.GeminiOptions{
				Endpoint: cfg.LLMEndpoint,
				Project:  cfg.LLMProject,
				Region:   cfg.LLMRegion,
			},
			Claude: llm.ClaudeOptions{
				Endpoint: cfg.LLMEndpoint,
				APIKey:   cfg.LLMAPIKey,
			},
		})
	default:
		client, err = llm.NewFakeClient(auditor, llm.NewFakeEngine(
			cfg.LLMEngineLabel, "bs-lm-fake", moments.DefaultFakeComposeResponse()))
	}
	if err != nil {
		return nil, err
	}
	return moments.Composer{
		Engine: moments.Engine{
			Gen:    client,
			Labels: moments.LangLabelResolver{Label: cfg.LLMEngineLabel},
		},
		Store: st,
	}, nil
}
