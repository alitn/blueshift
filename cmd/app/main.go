// Command app is the Blueshift API server with the embedded web build. It is
// intentionally thin: wiring only. All logic lives under internal/.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"blueshift/internal/config"
	"blueshift/internal/logx"
	"blueshift/internal/server"
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
	srv := server.New(cfg, logger, ui, ready)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return server.Run(ctx, srv, logger)
}
