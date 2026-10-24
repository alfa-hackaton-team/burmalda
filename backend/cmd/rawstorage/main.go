package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/database"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/rawartifacts"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var missingObjects = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "ingestion_raw_missing_objects", Help: "Database raw artifacts whose S3 content cannot be verified.",
})
var orphanObjects = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "ingestion_raw_orphan_objects", Help: "S3 raw objects without a database reference after the grace period.",
})
var reconciliationErrors = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "ingestion_raw_reconciliation_errors_total", Help: "Failed S3 raw artifact reconciliation cycles.",
})

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("raw storage command failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 || (os.Args[1] != "migrate" && os.Args[1] != "reconcile" && os.Args[1] != "init-bucket") {
		return fmt.Errorf("usage: rawstorage init-bucket | migrate [--dry-run] [--limit N] | reconcile [--once] [--interval 24h] [--grace 24h] [--metrics-addr :19094]")
	}
	mode := os.Args[1]
	flags := flag.NewFlagSet(mode, flag.ContinueOnError)
	dryRun := flags.Bool("dry-run", false, "count PostgreSQL originals without moving them")
	limit := flags.Int("limit", 1000, "maximum originals to migrate in this run; 0 means all")
	once := flags.Bool("once", false, "run one reconciliation cycle")
	interval := flags.Duration("interval", 24*time.Hour, "reconciliation interval")
	grace := flags.Duration("grace", 24*time.Hour, "age before reporting an unreferenced object")
	metricsAddr := flags.String("metrics-addr", "", "Prometheus listen address")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *limit < 0 || *interval <= 0 || *grace < 0 {
		return fmt.Errorf("limit, interval or grace is invalid")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.RawStorageBackend != "s3" {
		return fmt.Errorf("RAW_STORAGE_BACKEND=s3 is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := rawartifacts.NewS3(ctx, cfg)
	if err != nil {
		return err
	}
	if mode == "init-bucket" {
		if cfg.S3Endpoint == "" {
			return fmt.Errorf("init-bucket requires an explicit S3_ENDPOINT")
		}
		initCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		for {
			if err := store.EnsureBucket(initCtx); err == nil {
				slog.Info("S3 bucket is ready", "bucket", cfg.S3Bucket)
				return nil
			} else if initCtx.Err() != nil {
				return fmt.Errorf("initialize S3 bucket: %w", err)
			}
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-initCtx.Done():
				timer.Stop()
				return fmt.Errorf("initialize S3 bucket: %w", initCtx.Err())
			case <-timer.C:
			}
		}
	}
	db, err := database.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	if mode == "migrate" {
		return migrate(ctx, db, store, *dryRun, *limit)
	}
	if *metricsAddr != "" {
		registry := prometheus.NewRegistry()
		registry.MustRegister(missingObjects, orphanObjects, reconciliationErrors)
		server := &http.Server{Addr: *metricsAddr, Handler: promhttp.HandlerFor(registry, promhttp.HandlerOpts{}), ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("raw storage metrics server stopped", "error", err)
				stop()
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		}()
	}
	for {
		if err := reconcile(ctx, db, store, *grace); err != nil {
			reconciliationErrors.Inc()
			if *once {
				return err
			}
			slog.Error("raw storage reconciliation failed", "error", err)
		}
		if *once || ctx.Err() != nil {
			return nil
		}
		timer := time.NewTimer(*interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func migrate(ctx context.Context, db *pgxpool.Pool, store *rawartifacts.S3, dryRun bool, limit int) error {
	if dryRun {
		var count int64
		if err := db.QueryRow(ctx, `SELECT count(*) FROM raw_artifacts WHERE content IS NOT NULL`).Scan(&count); err != nil {
			return err
		}
		slog.Info("PostgreSQL originals eligible for migration", "count", count)
		return nil
	}
	var cursor string
	for moved := 0; limit == 0 || moved < limit; moved++ {
		var id, sourceID, contentType, expectedHash string
		var size int64
		var content []byte
		err := db.QueryRow(ctx, `SELECT id::text, source_id::text, content_type, sha256, size_bytes, content
			FROM raw_artifacts WHERE content IS NOT NULL AND ($1::uuid IS NULL OR id>$1::uuid)
			ORDER BY id LIMIT 1`, nullIfEmpty(cursor)).Scan(&id, &sourceID, &contentType, &expectedHash, &size, &content)
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Info("PostgreSQL originals migration finished", "moved", moved)
			return nil
		}
		if err != nil {
			return err
		}
		cursor = id
		sum := sha256.Sum256(content)
		if int64(len(content)) != size || hex.EncodeToString(sum[:]) != expectedHash {
			return fmt.Errorf("raw artifact %s has inconsistent bytes or metadata", id)
		}
		artifact, err := store.Prepare(ctx, sourceID, contentType, content)
		if err != nil {
			return fmt.Errorf("upload raw artifact %s: %w", id, err)
		}
		if err := store.Verify(ctx, artifact); err != nil {
			return fmt.Errorf("verify raw artifact %s: %w", id, err)
		}
		command, err := db.Exec(ctx, `UPDATE raw_artifacts SET content=NULL, object_uri=$2
			WHERE id=$1::uuid AND content IS NOT NULL AND sha256=$3 AND size_bytes=$4`,
			id, artifact.ObjectURI, artifact.SHA256, artifact.Size)
		if err != nil {
			return fmt.Errorf("move raw artifact %s reference: %w", id, err)
		}
		if command.RowsAffected() != 1 {
			return fmt.Errorf("raw artifact %s changed during migration", id)
		}
	}
	slog.Info("PostgreSQL originals migration limit reached", "limit", limit)
	return nil
}

func reconcile(ctx context.Context, db *pgxpool.Pool, store *rawartifacts.S3, grace time.Duration) error {
	rows, err := db.Query(ctx, `SELECT object_uri, sha256, content_type, size_bytes FROM raw_artifacts WHERE object_uri IS NOT NULL
		UNION ALL
		SELECT object_uri, sha256, 'application/pdf', size_bytes FROM dataset_bank_reports WHERE object_uri IS NOT NULL`)
	if err != nil {
		return err
	}
	referenced := make(map[string]rawartifacts.Artifact)
	for rows.Next() {
		var item rawartifacts.Artifact
		if err := rows.Scan(&item.ObjectURI, &item.SHA256, &item.ContentType, &item.Size); err != nil {
			rows.Close()
			return err
		}
		referenced[item.ObjectURI] = item
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	missing := 0
	for _, item := range referenced {
		if err := store.Verify(ctx, item); err != nil {
			missing++
			slog.Error("raw artifact cannot be verified", "object_uri", item.ObjectURI, "error", err)
		}
	}
	missingObjects.Set(float64(missing))
	orphans := 0
	cutoff := time.Now().Add(-grace)
	err = store.List(ctx, func(object rawartifacts.Object) error {
		if _, ok := referenced[object.URI]; !ok && object.LastModified.Before(cutoff) {
			orphans++
			slog.Warn("unreferenced raw artifact", "object_uri", object.URI, "last_modified", object.LastModified)
		}
		return nil
	})
	if err != nil {
		return err
	}
	orphanObjects.Set(float64(orphans))
	slog.Info("raw storage reconciliation finished", "referenced", len(referenced), "missing", missing, "orphans", orphans)
	return nil
}

func nullIfEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
