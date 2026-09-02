package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/2SSK/tenantflow/internal/metrics"
)

func TestMetricsMiddlewareLabelsByRoutePattern(t *testing.T) {
	reg := metrics.New()

	// The inner mux resembles the real router: a pattern with a path
	// parameter, so the middleware's route label must be the PATTERN.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/tenants/{tenantID}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	handler := Metrics(reg, mux)

	for _, path := range []string{"/api/v1/tenants/acme", "/api/v1/tenants/omega-corp"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	}

	text, err := reg.Export()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for _, want := range []string{
		`tenantflow_http_requests_total{method="GET",route="/api/v1/tenants/{tenantID}",status="404"} 2`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing series %q in:\n%s", want, text)
		}
	}
	// The two raw paths must collapse into one series (no cardinality blowup).
	if strings.Contains(text, `route="/api/v1/tenants/acme"`) {
		t.Fatalf("raw path leaked into route label:\n%s", text)
	}
}

func TestMetricsMiddlewareSkipsMetricsEndpoint(t *testing.T) {
	reg := metrics.New()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := Metrics(reg, mux)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	text, err := reg.Export()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if strings.Contains(text, "tenantflow_http_requests_total") {
		t.Fatalf("self-scrape should not be counted:\n%s", text)
	}
}