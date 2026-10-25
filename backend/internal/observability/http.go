package observability

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ingestion_http_requests_total", Help: "Incoming API requests by bounded route, method and HTTP status."}, []string{"route", "method", "code"})
	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "ingestion_http_request_duration_seconds", Help: "Incoming API handler duration including authentication and validation.", Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}}, []string{"route", "method"})
)

var apiRoutes = []string{
	"/health/live", "/health/ready", "/login", "/v0.1/auth/login", "/v0.1/auth/logout", "/v0.1/me", "/v0.1/contexts", "/v0.1/feed", "/v0.1/alerts", "/v0.1/insights", "/v0.1/insights/{insight_id}", "/v0.1/insights/{insight_id}/conversations", "/v0.1/insights/{insight_id}/feedback", "/v0.1/generations/{generation_id}", "/v0.1/conversations/{conversation_id}/messages", "/v0.1/traces/{trace_id}", "/v0.1/analytics/events", "/v0.1/admin/sources", "/v0.1/admin/sources/{source_id}", "/v0.1/admin/sources/{source_id}/syncs", "/v0.1/admin/source-syncs/{sync_id}", "/v0.1/admin/quality", "/internal/v0.1/datasets/active", "/internal/v0.1/datasets/active/{kind}",
}

func routeLabel(path string) string {
	parts := strings.Split(path, "/")
	for _, route := range apiRoutes {
		candidate := strings.Split(route, "/")
		if len(candidate) != len(parts) {
			continue
		}
		match := true
		for i, part := range candidate {
			if strings.HasPrefix(part, "{") {
				if parts[i] == "" {
					match = false
				}
			} else if part != parts[i] {
				match = false
			}
		}
		if match {
			return route
		}
	}
	return "unmatched"
}

func HTTPHandler(next http.Handler) http.Handler {
	handlers := map[string]http.Handler{}
	for _, route := range append(append([]string{}, apiRoutes...), "unmatched") {
		labels := prometheus.Labels{"route": route}
		handlers[route] = promhttp.InstrumentHandlerDuration(httpDuration.MustCurryWith(labels), promhttp.InstrumentHandlerCounter(httpRequests.MustCurryWith(labels), next))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handlers[routeLabel(r.URL.Path)].ServeHTTP(w, r) })
}

func HTTPCollectors() []prometheus.Collector {
	return []prometheus.Collector{httpRequests, httpDuration}
}

func StartMetrics(addr string, registry *prometheus.Registry, timeout time.Duration) (func(), error) {
	if addr == "" {
		return func() {}, nil
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: promhttp.HandlerFor(registry, promhttp.HandlerOpts{}), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics server failed", "error", err)
		}
	}()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
		}
	}, nil
}
