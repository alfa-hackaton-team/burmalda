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

var apiRoutes = []string{"/health/live", "/health/ready", "/v0.1/me", "/v0.1/contexts", "/internal/v0.1/datasets/active", "/internal/v0.1/datasets/active/{kind}"}

func routeLabel(path string) string {
	for _, route := range apiRoutes {
		if route == path {
			return route
		}
	}
	if rest, ok := strings.CutPrefix(path, "/internal/v0.1/datasets/active/"); ok && rest != "" && !strings.Contains(rest, "/") {
		return "/internal/v0.1/datasets/active/{kind}"
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
