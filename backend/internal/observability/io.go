package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	s3Operations     = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ingestion_s3_operations_total", Help: "S3 SDK operations including SDK retries, counted once per operation."}, []string{"operation", "outcome"})
	s3Duration       = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "ingestion_s3_operation_duration_seconds", Help: "S3 SDK operation duration; GET includes body consumption until Close.", Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60}}, []string{"operation", "outcome"})
	s3Bytes          = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ingestion_s3_transferred_bytes_total", Help: "Successful PUT payload bytes and consumed GET body bytes; excludes protocol overhead."}, []string{"direction"})
	upstreamDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "ingestion_upstream_http_duration_seconds", Help: "HTTP calls to feed, article and ML upstreams including consumed response body.", Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60}}, []string{"upstream", "outcome"})
	upstreamErrors   = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ingestion_upstream_http_errors_total", Help: "Transport, response body or size-limit failures; HTTP status errors are counted separately."}, []string{"upstream"})
)

func IOCollectors() []prometheus.Collector {
	return []prometheus.Collector{s3Operations, s3Duration, s3Bytes}
}

func S3Operation(operation string, started time.Time, err error) {
	outcome := "success"
	if err != nil {
		outcome = "error"
	}
	s3Operations.WithLabelValues(operation, outcome).Inc()
	s3Duration.WithLabelValues(operation, outcome).Observe(time.Since(started).Seconds())
}

func S3Bytes(direction string, n int64) {
	if n > 0 {
		s3Bytes.WithLabelValues(direction).Add(float64(n))
	}
}

func UpstreamTiming(name string, started time.Time, status int, err error) {
	outcome := "success"
	if err != nil {
		outcome = "transport_error"
		upstreamErrors.WithLabelValues(name).Inc()
	} else if status >= 400 {
		outcome = "http_error"
	}
	upstreamDuration.WithLabelValues(name, outcome).Observe(time.Since(started).Seconds())
}
