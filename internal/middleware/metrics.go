package middleware

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/2SSK/tenantflow/internal/metrics"
)

// Metrics counts every API request by method, route pattern and status.
//
// The route label comes from r.Pattern, which Go 1.22's ServeMux sets on
// the request as it dispatches — so a flood of tenant IDs collapses into
// the pattern "POST /api/v1/tenants/{tenantID}" instead of exploding label
// cardinality. /metrics itself is skipped so self-scrapes stay invisible.
func Metrics(reg *metrics.Registry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		route := r.Pattern
		if route == "" {
			route = r.URL.Path
		}
		// Go 1.22 mux patterns carry the method prefix ("GET /path"); the
		// method already has its own label, so keep the route label clean.
		if i := strings.IndexByte(route, ' '); i >= 0 {
			route = route[i+1:]
		}
		reg.HTTPRequests.WithLabelValues(r.Method, route, strconv.Itoa(sw.status)).Inc()
	})
}
