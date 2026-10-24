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
	"github.com/alfa-hackaton-team/burmalda/backend/internal/mljobs"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/polling"
)

type dynamicProcessor struct {
	manager         *config.Manager
	pipelineVersion string
	maxTextBytes    int
	fetcher         *feeds.SafeFetcher
	feedSettings    config.Runtime
}

func (p *dynamicProcessor) Poll(ctx context.Context, lease polling.Lease) (polling.Outcome, error) {
	settings := p.manager.Current()
	if p.fetcher == nil || settings.Feeds != p.feedSettings.Feeds {
		if p.fetcher != nil {
			p.fetcher.CloseIdleConnections()
		}
		p.fetcher = feeds.NewSafeFetcherWithSettings(nil, settings)
		p.feedSettings = settings
	}
	return (feeds.Processor{
		Fetcher:         p.fetcher,
		PipelineVersion: p.pipelineVersion, MaxTextBytes: p.maxTextBytes,
		Limits: &settings,
	}).Poll(ctx, lease)
}

func (p *dynamicProcessor) Close() {
	if p.fetcher != nil {
		p.fetcher.CloseIdleConnections()
	}
}

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
	manager := config.NewManagerFromConfig(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config.WatchSIGHUP(ctx, manager)

	pool, err := database.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	processor := &dynamicProcessor{manager: manager, pipelineVersion: cfg.MLPipelineVersion, maxTextBytes: cfg.MLMaxTextBytes}
	defer processor.Close()
	runner := polling.Runner{
		Store:     polling.NewStore(pool),
		Processor: processor,
		Settings:  manager.Current,
	}
	slog.Info("source worker started")
	if cfg.MLBaseURL == "" {
		return runner.Run(ctx)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errors := make(chan error, 2)
	go func() { errors <- runner.Run(workerCtx) }()
	go func() {
		jobStore := mljobs.NewStore(pool)
		jobStore.Settings = manager.Current
		errors <- (mljobs.Runner{
			Store:    jobStore,
			Client:   mljobs.Client{BaseURL: cfg.MLBaseURL, Token: cfg.MLServiceToken, Settings: manager.Current},
			Settings: manager.Current,
		}).Run(workerCtx)
	}()
	err = <-errors
	cancel()
	<-errors
	if err != nil {
		return err
	}
	slog.Info("worker stopped")
	return nil
}
