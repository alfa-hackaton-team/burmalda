package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/database"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/observability"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/rawartifacts"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("observer stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	manager := config.NewManagerFromConfig(cfg)
	config.WatchSIGHUP(ctx, manager)
	shutdownLogging, err := observability.InitLogging(ctx, "ingestion-observer")
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := shutdownLogging(shutdownCtx); err != nil {
			slog.Error("logging shutdown failed", "error", err)
		}
	}()
	db, err := database.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	registry := prometheus.NewRegistry()
	registry.MustRegister(observability.NewStateCollector(db))
	collectors := []*observability.StorageCollector{observability.NewStorageCollector("postgres", observability.PostgreSQLUsage(db))}
	if cfg.RawStorageBackend == "s3" {
		store, err := rawartifacts.NewS3(ctx, cfg)
		if err != nil {
			return err
		}
		collectors = append(collectors, observability.NewStorageCollector("s3", func(ctx context.Context) (observability.StorageUsage, error) {
			store.SetRequestTimeout(manager.Current().RawStorage.RequestTimeout.Std())
			usage, err := store.Usage(ctx)
			return observability.StorageUsage{Bytes: usage.Bytes, Objects: &usage.Objects}, err
		}))
	}
	collectionCtx, cancelCollection := context.WithCancel(ctx)
	var collectionWorkers sync.WaitGroup
	for _, collector := range collectors {
		registry.MustRegister(collector)
		collectionWorkers.Add(1)
		go func() {
			defer collectionWorkers.Done()
			collector.Run(collectionCtx, func() (time.Duration, time.Duration) {
				settings := manager.Current().StorageMetrics
				return settings.Interval.Std(), settings.Timeout.Std()
			})
		}()
	}
	defer func() { cancelCollection(); collectionWorkers.Wait() }()
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	addr := os.Getenv("METRICS_ADDR")
	if addr == "" {
		addr = ":19091"
	}
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- server.ListenAndServe() }()
	slog.Info("observer started", "addr", addr)
	select {
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
