package observability

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestHTTPMetricsHaveBoundedRoutesAndRealStatus(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(HTTPCollectors()...)
	handler := HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusRequestEntityTooLarge) }))
	for _, path := range []string{"/v0.1/me", "/v0.1/contexts", "/private/customer-secret", "/random/another-secret", "/internal/v0.1/datasets/active/payments"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", path, nil))
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	foundUnmatched := false
	for _, family := range families {
		if family.GetName() != "ingestion_http_requests_total" {
			continue
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() == "code" && label.GetValue() != "413" {
					t.Fatal("status was not captured")
				}
				if label.GetName() == "route" {
					if label.GetValue() != "unmatched" && routeLabel(label.GetValue()) != label.GetValue() {
						t.Fatalf("unbounded route %q", label.GetValue())
					}
					if label.GetValue() == "unmatched" {
						foundUnmatched = true
						if metric.Counter.GetValue() < 2 {
							t.Fatal("unknown routes not grouped")
						}
					}
				}
			}
		}
	}
	if !foundUnmatched {
		t.Fatal("unknown route was not measured")
	}
}
