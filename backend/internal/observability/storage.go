package observability

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

type StorageUsage struct {
	Bytes   int64
	Objects *int64
}

type StorageCollector struct {
	read                                                   func(context.Context) (StorageUsage, error)
	mu                                                     sync.RWMutex
	usage                                                  StorageUsage
	up                                                     bool
	lastSuccess                                            time.Time
	storage                                                string
	usedBytes, objects, collectionUp, lastSuccessTimestamp *prometheus.Desc
}

func NewStorageCollector(storage string, read func(context.Context) (StorageUsage, error)) *StorageCollector {
	labels := prometheus.Labels{"storage": storage}
	return &StorageCollector{read: read, storage: storage,
		usedBytes:            prometheus.NewDesc("ingestion_storage_used_bytes", "PostgreSQL database size or current S3 object bytes; excludes WAL and S3 version history, respectively.", nil, labels),
		objects:              prometheus.NewDesc("ingestion_storage_objects", "Number of current objects in the configured S3 bucket.", nil, labels),
		collectionUp:         prometheus.NewDesc("ingestion_storage_collection_up", "Whether the most recent storage inventory succeeded.", nil, labels),
		lastSuccessTimestamp: prometheus.NewDesc("ingestion_storage_last_success_timestamp_seconds", "Unix timestamp of the most recent complete storage inventory.", nil, labels),
	}
}

func PostgreSQLUsage(db *pgxpool.Pool) func(context.Context) (StorageUsage, error) {
	return func(ctx context.Context) (StorageUsage, error) {
		var usage StorageUsage
		err := db.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&usage.Bytes)
		return usage, err
	}
}

func (c *StorageCollector) Run(ctx context.Context, settings func() (time.Duration, time.Duration)) {
	for ctx.Err() == nil {
		interval, timeout := settings()
		requestCtx, cancel := context.WithTimeout(ctx, timeout)
		usage, err := c.read(requestCtx)
		cancel()
		c.record(usage, err, time.Now())
		if err != nil && ctx.Err() == nil {
			slog.Warn("storage inventory failed", "storage", c.storage, "error", err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *StorageCollector) record(usage StorageUsage, err error, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.up = err == nil
	if c.up {
		c.usage, c.lastSuccess = usage, now
	}
}

func (c *StorageCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{c.usedBytes, c.objects, c.collectionUp, c.lastSuccessTimestamp} {
		ch <- desc
	}
}

func (c *StorageCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	up := 0.0
	if c.up {
		up = 1
	}
	ch <- prometheus.MustNewConstMetric(c.collectionUp, prometheus.GaugeValue, up)
	if !c.lastSuccess.IsZero() {
		ch <- prometheus.MustNewConstMetric(c.lastSuccessTimestamp, prometheus.GaugeValue, float64(c.lastSuccess.Unix()))
	}
	if !c.up {
		return
	}
	ch <- prometheus.MustNewConstMetric(c.usedBytes, prometheus.GaugeValue, float64(c.usage.Bytes))
	if c.usage.Objects != nil {
		ch <- prometheus.MustNewConstMetric(c.objects, prometheus.GaugeValue, float64(*c.usage.Objects))
	}
}
