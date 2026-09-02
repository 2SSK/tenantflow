// Package metrics exposes Prometheus metrics for the control plane.
//
// One /metrics endpoint per process is fed from three sources:
//
//  1. Custom tenantflow_* counters and gauges registered here;
//  2. Temporal SDK metrics (temporal_*) adopted through a thin
//     client.MetricsHandler adapter — the SDK ships no Prometheus
//     implementation in v1.47.0, so we implement its small interface
//     ourselves instead of pulling in a stale contrib module;
//  3. Standard Go runtime and process collectors (goroutines, RSS, ...).
//
// The API and the worker each build their own Registry, so the two
// processes are scraped separately and can be scaled independently.
package metrics

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"
	"go.temporal.io/sdk/client"
)

// timerBuckets covers Temporal's latency timers: activity latencies are
// sub-second, workflow runs span minutes (the delete grace period is 60s).
var timerBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}

// Registry owns the process's Prometheus registry plus the custom
// tenantflow_* collectors shared by the HTTP middleware and the
// activity interceptor.
type Registry struct {
	prom *prometheus.Registry

	// mu guards the per-name collector caches; the SDK emits metrics from
	// many workflow goroutines at once.
	mu       sync.Mutex
	counters map[string]*prometheus.CounterVec
	gauges   map[string]*prometheus.GaugeVec
	timers   map[string]*prometheus.HistogramVec

	// HTTPRequests counts API calls by method, route pattern and status.
	// The route label is the ServeMux pattern ("POST /api/v1/tenants/{tenantID}"),
	// never the raw path, so a flood of tenant IDs cannot explode cardinality.
	HTTPRequests *prometheus.CounterVec

	// ActivityExecutions counts worker activity executions by activity name
	// and result ("completed" | "failed"). Attempts are counted as they
	// happen, so retries are visible — useful for the fail-matrix story.
	ActivityExecutions *prometheus.CounterVec
}

// New builds a Registry with the custom collectors and the Go/process
// collectors registered.
func New() *Registry {
	prom := prometheus.NewRegistry()
	r := &Registry{
		prom:     prom,
		counters: make(map[string]*prometheus.CounterVec),
		gauges:   make(map[string]*prometheus.GaugeVec),
		timers:   make(map[string]*prometheus.HistogramVec),
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tenantflow_http_requests_total",
			Help: "API requests by method, route pattern and response status.",
		}, []string{"method", "route", "status"}),
		ActivityExecutions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tenantflow_activity_executions_total",
			Help: "Worker activity executions by activity and result.",
		}, []string{"activity", "result"}),
	}
	r.mustRegister(
		r.HTTPRequests,
		r.ActivityExecutions,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return r
}

// Handler serves the Prometheus text exposition format for scraping.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.prom, promhttp.HandlerOpts{})
}

// Export renders the registry as Prometheus exposition text — used by
// tests to assert on exact series and labels.
func (r *Registry) Export() (string, error) {
	mfs, err := r.prom.Gather()
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	enc := expfmt.NewEncoder(&sb, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfs {
		if err := enc.Encode(mf); err != nil {
			return "", err
		}
	}
	return sb.String(), nil
}

func (r *Registry) mustRegister(cs ...prometheus.Collector) {
	r.prom.MustRegister(cs...)
}

// TemporalHandler adapts the Temporal SDK's metrics interface to our
// Prometheus registry. The SDK calls WithTags with its tag conventions
// (namespace, workflow_type, activity_type, task_queue, ...), then asks
// for counters/gauges/timers by name; every adapter call just re-labels
// a Prometheus collector.
type TemporalHandler struct {
	reg  *Registry
	tags map[string]string
}

// Compile-time proof that we satisfy the SDK's public metrics interface.
var _ client.MetricsHandler = (*TemporalHandler)(nil)

// NewTemporalHandler wires the SDK metrics into the given registry.
func NewTemporalHandler(reg *Registry) *TemporalHandler {
	return &TemporalHandler{reg: reg, tags: make(map[string]string)}
}

// WithTags returns a handler with the given tags merged over the current
// ones. The SDK preserves old tags unless a key is overwritten.
func (h *TemporalHandler) WithTags(tags map[string]string) client.MetricsHandler {
	merged := make(map[string]string, len(h.tags)+len(tags))
	for k, v := range h.tags {
		merged[k] = v
	}
	for k, v := range tags {
		merged[k] = v
	}
	return &TemporalHandler{reg: h.reg, tags: merged}
}

func (h *TemporalHandler) Counter(name string) client.MetricsCounter {
	cv := h.reg.counterVec(name, sortedLabelNames(h.tags))
	c := cv.WithLabelValues(labelValues(h.tags)...)
	return counterFunc(func(n int64) { c.Add(float64(n)) })
}

func (h *TemporalHandler) Gauge(name string) client.MetricsGauge {
	gv := h.reg.gaugeVec(name, sortedLabelNames(h.tags))
	g := gv.WithLabelValues(labelValues(h.tags)...)
	return gaugeFunc(func(v float64) { g.Set(v) })
}

func (h *TemporalHandler) Timer(name string) client.MetricsTimer {
	hv := h.reg.timerVec(name, sortedLabelNames(h.tags))
	o := hv.WithLabelValues(labelValues(h.tags)...)
	return timerFunc(func(d time.Duration) { o.Observe(d.Seconds()) })
}

type counterFunc func(int64)

func (f counterFunc) Inc(n int64) { f(n) }

type gaugeFunc func(float64)

func (f gaugeFunc) Update(v float64) { f(v) }

type timerFunc func(time.Duration)

func (f timerFunc) Record(d time.Duration) { f(d) }

// sortedLabelNames returns the tag keys.
func sortedLabelNames(tags map[string]string) []string {
	names := make([]string, 0, len(tags))
	for k := range tags {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// labelValues returns the tag values in sortedLabelNames order, matching
// the WithLabelValues arity of the label set the vec was created with.
func labelValues(tags map[string]string) []string {
	names := sortedLabelNames(tags)
	vals := make([]string, len(names))
	for i, k := range names {
		vals[i] = tags[k]
	}
	return vals
}

// counterVec returns (creating on first use) a CounterVec for the SDK
// metric name and a fixed, sorted label set. The SDK's tag set per metric
// name is stable (defined for each metric in its tags.go), so per-name
// collection is safe; later calls with the identical key reuse the vec.
// The registry lock guards creation because the SDK emits metrics from
// many workflow goroutines at once.
func (r *Registry) counterVec(name string, labelNames []string) *prometheus.CounterVec {
	key := name + "|" + strings.Join(labelNames, ",")
	r.mu.Lock()
	defer r.mu.Unlock()
	if cv, ok := r.counters[key]; ok {
		return cv
	}
	cv := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: name,
		Help: "Temporal SDK counter " + name,
	}, labelNames)
	r.counters[key] = cv
	r.prom.MustRegister(cv)
	return cv
}

func (r *Registry) gaugeVec(name string, labelNames []string) *prometheus.GaugeVec {
	key := name + "|" + strings.Join(labelNames, ",")
	r.mu.Lock()
	defer r.mu.Unlock()
	if gv, ok := r.gauges[key]; ok {
		return gv
	}
	gv := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: name,
		Help: "Temporal SDK gauge " + name,
	}, labelNames)
	r.gauges[key] = gv
	r.prom.MustRegister(gv)
	return gv
}

func (r *Registry) timerVec(name string, labelNames []string) *prometheus.HistogramVec {
	key := name + "|" + strings.Join(labelNames, ",")
	r.mu.Lock()
	defer r.mu.Unlock()
	if hv, ok := r.timers[key]; ok {
		return hv
	}
	hv := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    name,
		Help:    "Temporal SDK timer " + name,
		Buckets: timerBuckets,
	}, labelNames)
	r.timers[key] = hv
	r.prom.MustRegister(hv)
	return hv
}
