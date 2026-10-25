package main

import (
	"context"
	"crypto/sha256"
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
	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpsecurity"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/observability"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/sources"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/userapi"
	"github.com/prometheus/client_golang/prometheus"
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
	registry := prometheus.NewRegistry()
	registry.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}), observability.NewPoolCollector(pool))
	registry.MustRegister(observability.HTTPCollectors()...)
	stopMetrics, err := observability.StartMetrics(os.Getenv("METRICS_ADDR"), registry, cfg.ShutdownTimeout)
	if err != nil {
		return err
	}
	defer stopMetrics()
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
	userStore.Settings = manager.Current
	if readURL := os.Getenv("ML_READ_DATABASE_URL"); readURL != "" {
		readCfg := cfg
		readCfg.DatabaseURL = readURL
		readPool, err := database.Open(ctx, readCfg)
		if err != nil {
			return err
		}
		defer readPool.Close()
		userStore.SetCorpusPool(readPool)
	}
	userStore.ConfigureDemo(os.Getenv("DEMO_INSIGHT_SET_VERSION"))
	userStore.ConfigureML(cfg.MLBaseURL, cfg.MLServiceToken, os.Getenv("ML_ANALYSIS_CONFIG_VERSION"))
	analysisCtx, stopAnalyses := context.WithCancel(ctx)
	analysisDone := make(chan struct{})
	go func() { defer close(analysisDone); _ = userStore.RunAnalyses(analysisCtx) }()
	defer func() { stopAnalyses(); <-analysisDone }()
	cursorSigningKey, err := userapi.ParseCursorSigningKey(os.Getenv("CURSOR_SIGNING_KEY"))
	if err != nil {
		return err
	}
	allowedOrigins, err := httpsecurity.ParseOriginAllowlist(os.Getenv("API_ALLOWED_ORIGINS"))
	if err != nil {
		return err
	}
	serverSettings := manager.Current().Server
	apiHandler, err := newAPIHandler(sessionStore, userStore,
		sources.NewStore(pool, sources.Validator{}), sources.NewPostgresAuthorizer(pool), cursorSigningKey,
		httpsecurity.Options{
			RequestTimeout: serverSettings.RequestTimeout.Std(),
			MaxBodyBytes:   serverSettings.MaxBodyBytes,
			AllowedOrigins: allowedOrigins,
		})
	if err != nil {
		return err
	}
	mux.Handle("/login", userapi.LoginPage(os.Getenv("AUTH_PASSWORD_LOGIN_ENABLED") == "true"))
	mux.Handle("/v0.1/auth/login", httpsecurity.Protect(userStore.LoginHandler(allowedOrigins, os.Getenv("AUTH_PASSWORD_LOGIN_ENABLED") == "true", os.Getenv("AUTH_COOKIE_SECURE") != "false"), httpsecurity.Options{RequestTimeout: serverSettings.RequestTimeout.Std(), MaxBodyBytes: 16 << 10, AllowedOrigins: allowedOrigins}))
	mux.Handle("/v0.1/", apiHandler)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           observability.HTTPHandler(mux),
		ReadHeaderTimeout: manager.Current().Server.ReadHeaderTimeout.Std(),
		ReadTimeout:       serverSettings.ReadTimeout.Std(),
		WriteTimeout:      serverSettings.WriteTimeout.Std(),
		IdleTimeout:       serverSettings.IdleTimeout.Std(),
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

func newAPIHandler(sessions authn.Store, users userapi.Store, registry sources.Registry, authorize sources.Authorize, cursorKey [sha256.Size]byte, options httpsecurity.Options) (http.Handler, error) {
	sourceHandler, err := sources.AdminHandler(registry, authorize, cursorKey)
	if err != nil {
		return nil, err
	}
	routes := http.NewServeMux()
	routes.Handle("/v0.1/admin/sources", sourceHandler)
	routes.Handle("/v0.1/admin/sources/", sourceHandler)
	routes.Handle("/v0.1/admin/", sourceHandler)
	routes.Handle("/v0.1/", userapi.Handler(users, cursorKey))
	return httpsecurity.Protect(authn.Middleware(sessions,
		httpsecurity.RequireMutationProof(routes, options.AllowedOrigins)), options), nil
}
