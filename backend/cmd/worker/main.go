package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/database"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/feeds"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/polling"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker stopped", "error", err)
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

	pool, err := database.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	runner := polling.Runner{
		Store:         polling.NewStore(pool),
		Processor:     feeds.Processor{Fetcher: feeds.NewSafeFetcher(nil)},
		LeaseDuration: cfg.SourcePollLease,
		IdleInterval:  cfg.SourcePollIdle,
		ReapBatch:     cfg.SourcePollReapBatch,
	}
	slog.Info("source worker started")
	if err := runner.Run(ctx); err != nil {
		return err
	}
	slog.Info("worker stopped")
	return nil
}
