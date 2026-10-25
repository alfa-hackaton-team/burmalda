package observability

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

type PipelineCollector struct {
	db                                     *pgxpool.Pool
	up, jobs, durations, samples, failures *prometheus.Desc
}

func NewPipelineCollector(db *pgxpool.Pool) *PipelineCollector {
	return &PipelineCollector{db: db,
		up:        prometheus.NewDesc("ingestion_pipeline_collection_up", "Whether pipeline timing and throughput were collected successfully.", nil, nil),
		jobs:      prometheus.NewDesc("ingestion_queue_jobs_last_5m", "Persisted queue arrivals and terminal completions in the last five minutes; gauge, not a counter.", []string{"queue", "event"}, nil),
		durations: prometheus.NewDesc("ingestion_document_processing_seconds", "Duration quantiles of operations completed in the last hour; ingest_to_search is only successful operations and includes pre-enqueue delay.", []string{"stage", "status", "quantile"}, nil),
		samples:   prometheus.NewDesc("ingestion_document_processing_samples_last_hour", "Number of operations contributing to processing duration quantiles.", []string{"stage", "status"}, nil),
		failures:  prometheus.NewDesc("ingestion_ml_failures_last_hour", "Terminal failed ML jobs in the last hour, grouped by bounded error codes; gauge, not a counter.", []string{"code"}, nil),
	}
}

func (c *PipelineCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{c.up, c.jobs, c.durations, c.samples, c.failures} {
		ch <- desc
	}
}

func mlFailureCode(code string) string {
	switch code {
	case "VALIDATION_ERROR", "NOT_FOUND", "FORBIDDEN_CONTEXT", "SOURCE_UNAVAILABLE", "ML_PROCESSING_ERROR", "INSUFFICIENT_EVIDENCE", "TIMEOUT", "BUDGET_EXHAUSTED", "CONFLICT", "PROFILE_NOT_READY", "UNSUPPORTED_ADAPTER", "UNSUPPORTED_PIPELINE_VERSION", "PAYLOAD_TOO_LARGE", "INTERNAL_ERROR", "UNAUTHENTICATED", "FORBIDDEN", "ML_TRANSPORT_ERROR", "INVALID_ML_RESPONSE", "INVALID_ML_REQUEST", "PROCESSING_TIMEOUT", "SOURCE_SNAPSHOT_UNVERIFIED":
		return code
	case "":
		return "UNKNOWN"
	default:
		return "OTHER"
	}
}

func (c *PipelineCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	metrics, err := c.read(ctx)
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 1)
	for _, metric := range metrics {
		ch <- metric
	}
}

func (c *PipelineCollector) read(ctx context.Context) ([]prometheus.Metric, error) {
	metrics := []prometheus.Metric{}
	now := time.Now().UTC()
	for _, queue := range []struct{ name, table string }{{"article", "article_fetch_jobs"}, {"ml", "processing_jobs"}} {
		var arrivals, success, failure int64
		err := c.db.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM `+queue.table+` WHERE created_at >= $1 AND created_at <= $2),
 count(*) FILTER (WHERE status='SUCCEEDED'), count(*) FILTER (WHERE status IN ('FAILED','DEAD'))
 FROM `+queue.table+` WHERE completed_at >= $1 AND completed_at <= $2`, now.Add(-5*time.Minute), now).Scan(&arrivals, &success, &failure)
		if err != nil {
			return nil, err
		}
		for event, n := range map[string]int64{"arrived": arrivals, "succeeded": success, "failed": failure} {
			metrics = append(metrics, prometheus.MustNewConstMetric(c.jobs, prometheus.GaugeValue, float64(n), queue.name, event))
		}
	}
	rows, err := c.db.Query(ctx, `WITH recent AS MATERIALIZED (
 SELECT j.status, extract(epoch FROM j.completed_at-j.created_at)::double precision AS queue_seconds,
 extract(epoch FROM j.completed_at-v.fetched_at)::double precision AS ingest_seconds
 FROM processing_jobs j JOIN article_versions v ON v.id=j.article_version_id
 WHERE j.completed_at >= $1 AND j.completed_at <= $2
 ), durations AS (
 SELECT 'queue_to_terminal' AS stage, status, queue_seconds AS seconds FROM recent WHERE queue_seconds>=0
 UNION ALL SELECT 'ingest_to_search', status, ingest_seconds FROM recent WHERE status='SUCCEEDED' AND ingest_seconds>=0
 ) SELECT stage,status,count(*),percentile_cont(ARRAY[0.5,0.95,0.99]) WITHIN GROUP (ORDER BY seconds)
 FROM durations GROUP BY stage,status`, now.Add(-time.Hour), now)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var stage, status string
		var count int64
		var quantiles []float64
		if err := rows.Scan(&stage, &status, &count, &quantiles); err != nil {
			rows.Close()
			return nil, err
		}
		metrics = append(metrics, prometheus.MustNewConstMetric(c.samples, prometheus.GaugeValue, float64(count), stage, status))
		for i, q := range []float64{.5, .95, .99} {
			metrics = append(metrics, prometheus.MustNewConstMetric(c.durations, prometheus.GaugeValue, quantiles[i], stage, status, strconv.FormatFloat(q, 'f', -1, 64)))
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = c.db.Query(ctx, `SELECT coalesce(last_error_code,''),count(*) FROM processing_jobs
 WHERE status='FAILED' AND completed_at >= $1 AND completed_at <= $2 GROUP BY last_error_code`, now.Add(-time.Hour), now)
	if err != nil {
		return nil, err
	}
	failures := map[string]float64{}
	for rows.Next() {
		var code string
		var count int64
		if err := rows.Scan(&code, &count); err != nil {
			rows.Close()
			return nil, err
		}
		failures[mlFailureCode(code)] += float64(count)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for code, n := range failures {
		metrics = append(metrics, prometheus.MustNewConstMetric(c.failures, prometheus.GaugeValue, n, code))
	}
	return metrics, nil
}
