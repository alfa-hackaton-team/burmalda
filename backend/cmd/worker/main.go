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

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/database"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/feeds"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/mljobs"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/observability"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/polling"
)

type dynamicProcessor struct {
	manager         *config.Manager
	pipelineVersion string
	maxTextBytes    int
	fetcher         *feeds.SafeFetcher
	feedSettings    config.Runtime
}

type dynamicArticleProcessor struct {
	manager         *config.Manager
	pipelineVersion string
	maxTextBytes    int
	fetcher         *feeds.SafeFetcher
	feedSettings    config.Runtime
}

func (p *dynamicArticleProcessor) Process(ctx context.Context, job feeds.ArticleJob) feeds.ArticleOutcome {
	settings := p.manager.Current()
	if p.fetcher == nil || settings.Feeds != p.feedSettings.Feeds {
		if p.fetcher != nil {
			p.fetcher.CloseIdleConnections()
		}
		p.fetcher = feeds.NewSafeFetcherWithSettings(nil, settings)
		p.feedSettings = settings
	}
	return (feeds.ArticleJobProcessor{Fetcher: p.fetcher, Limits: settings,
		PipelineVersion: p.pipelineVersion, MaxTextBytes: p.maxTextBytes}).Process(ctx, job)
}

func (p *dynamicArticleProcessor) Close() {
	if p.fetcher != nil {
		p.fetcher.CloseIdleConnections()
	}
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
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
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
	shutdownLogging, err := observability.InitLogging(ctx, "ingestion-worker")
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
	shutdownTracing, err := observability.InitTracing(ctx, "ingestion-worker")
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			slog.Error("tracing shutdown failed", "error", err)
		}
	}()

	pool, err := database.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	processor := &dynamicProcessor{manager: manager, pipelineVersion: cfg.MLPipelineVersion, maxTextBytes: cfg.MLMaxTextBytes}
	defer processor.Close()
	articleProcessor := &dynamicArticleProcessor{manager: manager, pipelineVersion: cfg.MLPipelineVersion, maxTextBytes: cfg.MLMaxTextBytes}
	defer articleProcessor.Close()
	pollStore := polling.NewStore(pool)
	pollStore.Settings = manager.Current
	runner := polling.Runner{
		Store:     pollStore,
		Processor: processor,
		Settings:  manager.Current,
	}
	articleStore := feeds.NewArticleJobStore(pool)
	articleStore.Settings = manager.Current
	articleRunner := feeds.ArticleJobRunner{Store: articleStore, Processor: articleProcessor.Process,
		Settings: manager.Current, PipelineVersion: cfg.MLPipelineVersion}
	slog.Info("source worker started")
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runErrors := make(chan error, 4)
	workers := 2
	if addr := os.Getenv("METRICS_ADDR"); addr != "" {
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", observability.WorkerHandler())
		server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		workers++
		go func() {
			err := server.ListenAndServe()
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			runErrors <- err
		}()
		go func() {
			<-workerCtx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				slog.Error("metrics server shutdown failed", "error", err)
			}
		}()
		slog.Info("metrics server started", "addr", addr)
	}
	go func() { runErrors <- runner.Run(workerCtx) }()
	go func() { runErrors <- articleRunner.Run(workerCtx) }()
	if cfg.MLBaseURL != "" {
		workers++
		go func() {
			jobStore := mljobs.NewStore(pool)
			jobStore.Settings = manager.Current
			runErrors <- (mljobs.Runner{
				Store:    jobStore,
				Client:   mljobs.Client{BaseURL: cfg.MLBaseURL, Token: cfg.MLServiceToken, Settings: manager.Current},
				Settings: manager.Current,
			}).Run(workerCtx)
		}()
	}
	err = <-runErrors
	cancel()
	for i := 1; i < workers; i++ {
		<-runErrors
	}
	if err != nil {
		return err
	}
	slog.Info("worker stopped")
	return nil
}
