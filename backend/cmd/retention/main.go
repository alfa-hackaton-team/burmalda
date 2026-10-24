package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/database"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/retention"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	deleted     = prometheus.NewCounter(prometheus.CounterOpts{Name: "ingestion_retention_source_polls_deleted_total", Help: "Expired completed source polls deleted."})
	failures    = prometheus.NewCounter(prometheus.CounterOpts{Name: "ingestion_retention_errors_total", Help: "Failed retention cycles."})
	lastSuccess = prometheus.NewGauge(prometheus.GaugeOpts{Name: "ingestion_retention_last_success_timestamp_seconds", Help: "Time of the last successful retention cycle."})
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("retention stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	once := flag.Bool("once", false, "run one cleanup cycle and exit")
	dryRun := flag.Bool("dry-run", false, "count eligible polls without deleting")
	flag.Parse()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	manager := config.NewManagerFromConfig(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config.WatchSIGHUP(ctx, manager)
	db, err := database.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	store := retention.NewStore(db)
	if *dryRun {
		settings := manager.Current().Retention
		cutoff := retention.Cutoff(time.Now(), settings.SourcePollDays)
		countCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		count, err := store.Count(countCtx, cutoff)
		cancel()
		if err != nil {
			return err
		}
		slog.Info("retention dry run", "source_polls_candidates", count, "cutoff", cutoff)
		return nil
	}
	if *once {
		return cleanup(ctx, store, manager)
	}
	serverErrors := make(chan error, 1)
	if addr := os.Getenv("METRICS_ADDR"); addr != "" {
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		registry := prometheus.NewRegistry()
		registry.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}), deleted, failures, lastSuccess)
		server := &http.Server{Addr: addr, Handler: promhttp.HandlerFor(registry, promhttp.HandlerOpts{}), ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("retention metrics server failed", "error", err)
				serverErrors <- err
				stop()
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				slog.Error("retention metrics server shutdown failed", "error", err)
			}
		}()
	}
	for {
		if err := cleanup(ctx, store, manager); err != nil && ctx.Err() == nil {
			failures.Inc()
			slog.Error("retention cycle failed", "error", err)
		}
		if ctx.Err() != nil {
			select {
			case err := <-serverErrors:
				return err
			default:
			}
			return nil
		}
		timer := time.NewTimer(manager.Current().Retention.Interval.Std())
		select {
		case <-ctx.Done():
			timer.Stop()
			select {
			case err := <-serverErrors:
				return err
			default:
			}
			return nil
		case <-timer.C:
		}
	}
}

func cleanup(ctx context.Context, store *retention.Store, manager *config.Manager) error {
	settings := manager.Current().Retention
	cutoff := retention.Cutoff(time.Now(), settings.SourcePollDays)
	var total int64
	for {
		batchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		count, err := store.DeleteBatch(batchCtx, cutoff, settings.BatchSize)
		cancel()
		if err != nil {
			return err
		}
		total += count
		deleted.Add(float64(count))
		if count == 0 {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	lastSuccess.SetToCurrentTime()
	slog.Info("retention cycle completed", "source_polls_deleted", total, "cutoff", cutoff)
	return nil
}
