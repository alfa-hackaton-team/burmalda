package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	polls           = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ingestion_source_polls_total", Help: "Completed source polls."}, []string{"status"})
	pollDuration    = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "ingestion_source_poll_duration_seconds", Help: "Source poll duration.", Buckets: prometheus.DefBuckets}, []string{"status"})
	pollEntries     = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ingestion_source_poll_entries_total", Help: "Entries handled during source polls."}, []string{"kind"})
	articleJobs     = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ingestion_article_jobs_total", Help: "Completed article fetch attempts."}, []string{"status"})
	articleDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "ingestion_article_job_duration_seconds", Help: "Article fetch attempt duration.", Buckets: prometheus.DefBuckets}, []string{"status"})
	mlJobs          = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ingestion_ml_jobs_total", Help: "Completed ML job attempts."}, []string{"status"})
	mlDuration      = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "ingestion_ml_job_duration_seconds", Help: "ML job attempt duration.", Buckets: prometheus.DefBuckets}, []string{"status"})
	upstream        = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ingestion_upstream_http_responses_total", Help: "Upstream responses observed by ingestion workers."}, []string{"upstream", "status"})
	recovered       = prometheus.NewCounter(prometheus.CounterOpts{Name: "ingestion_source_leases_recovered_total", Help: "Source polls recovered after lease expiry."})
)

func WorkerHandler(extra ...prometheus.Collector) http.Handler {
	registry := prometheus.NewRegistry()
	registry.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
		polls, pollDuration, pollEntries, articleJobs, articleDuration, mlJobs, mlDuration, upstream, recovered, upstreamDuration, upstreamErrors)
	registry.MustRegister(IOCollectors()...)
	registry.MustRegister(extra...)
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

func Poll(status string, duration time.Duration, discovered, articles, versions, failed int) {
	polls.WithLabelValues(status).Inc()
	pollDuration.WithLabelValues(status).Observe(duration.Seconds())
	for kind, count := range map[string]int{"discovered": discovered, "new_articles": articles, "new_versions": versions, "failed": failed} {
		if count > 0 {
			pollEntries.WithLabelValues(kind).Add(float64(count))
		}
	}
}

func Article(status string, duration time.Duration) {
	articleJobs.WithLabelValues(status).Inc()
	articleDuration.WithLabelValues(status).Observe(duration.Seconds())
}

func ML(status string, duration time.Duration) {
	mlJobs.WithLabelValues(status).Inc()
	mlDuration.WithLabelValues(status).Observe(duration.Seconds())
}

func Upstream(name string, status int) {
	if status > 0 {
		upstream.WithLabelValues(name, strconv.Itoa(status)).Inc()
	}
}

func Recovered(n int64) {
	if n > 0 {
		recovered.Add(float64(n))
	}
}
