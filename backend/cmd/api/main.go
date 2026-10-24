package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/authn"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/database"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/dataset"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/health"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/userapi"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	manager := config.NewManagerFromConfig(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config.WatchSIGHUP(ctx, manager)

	pool, err := database.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	if os.Getenv("DATASET_REQUIRED") == "true" {
		if err := dataset.RequireActive(ctx, pool); err != nil {
			return err
		}
	}
	mux := http.NewServeMux()
	mux.Handle("/health/", health.HandlerWithTimeout(pool, func() time.Duration { return manager.Current().Server.ReadinessTimeout.Std() }))
	dataset.DatasetHandler{DB: pool, Token: os.Getenv("DATASET_API_TOKEN")}.Register(mux)
	sessionStore := authn.NewPostgresStore(pool)
	userStore := userapi.NewPostgresStore(pool)
	mux.Handle("/v0.1/", authn.Middleware(sessionStore, userapi.Handler(userStore)))

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: manager.Current().Server.ReadHeaderTimeout.Std(),
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	slog.Info("api started", "addr", cfg.HTTPAddr)

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), manager.Current().Server.ShutdownTimeout.Std())
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		slog.Info("api stopped")
		return nil
	}
}
