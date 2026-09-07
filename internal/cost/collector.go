package cost

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// TenantEstimate pairs the measured metadata of one tenant with its
// computed monthly estimate. It is the unit the Prometheus collector emits
// and the API endpoint returns.
type TenantEstimate struct {
	TenantID  string    `json:"tenantID"`
	Resources Resources `json:"resources"`
	Estimate  Estimate  `json:"estimate"`
}

// Loader supplies the measured metadata for every tenant. The API wires
// this to the repository; keeping the loader behind a function means the
// cost package stays free of database or HTTP concerns.
type Loader func(ctx context.Context) ([]TenantEstimate, error)

// Collector is a Prometheus collector that recomputes the per-tenant cost
// estimates at every scrape. Unlike a periodically-updated gauge, a
// per-scrape collector never leaves stale series behind for tenants that
// have since been deleted — the classic cardinality leak of naive
// cost dashboards.
//
// It follows the exporter contract: Describe announces the metric names,
// Collect queries the loader and emits one series per tenant. If the
// loader fails (database down), Collect emits nothing rather than a
// garbage value; the scrape simply honors the previous samples' staleness.
type Collector struct {
	loader Loader
	log    *slog.Logger

	total      *prometheus.Desc
	storage    *prometheus.Desc
	compute    *prometheus.Desc
	memory     *prometheus.Desc
	workflows  *prometheus.Desc
	backups    *prometheus.Desc
	storageB   *prometheus.Desc
	workflowCt *prometheus.Desc
}

// NewCollector builds a cost collector. Each metric is labeled by tenant.
func NewCollector(loader Loader, log *slog.Logger) *Collector {
	if log == nil {
		log = slog.Default()
	}
	labels := []string{"tenant"}
	return &Collector{
		loader:     loader,
		log:        log,
		total:      prometheus.NewDesc("tenantflow_tenant_monthly_cost_dollars", "Estimated monthly cost in dollars for this tenant.", labels, nil),
		storage:    prometheus.NewDesc("tenantflow_tenant_storage_cost_dollars", "Estimated monthly storage cost.", labels, nil),
		compute:    prometheus.NewDesc("tenantflow_tenant_compute_cost_dollars", "Estimated monthly compute cost.", labels, nil),
		memory:     prometheus.NewDesc("tenantflow_tenant_memory_cost_dollars", "Estimated monthly memory cost.", labels, nil),
		workflows:  prometheus.NewDesc("tenantflow_tenant_workflow_cost_dollars", "Estimated monthly workflow-execution cost.", labels, nil),
		backups:    prometheus.NewDesc("tenantflow_tenant_backup_cost_dollars", "Estimated monthly backup storage cost.", labels, nil),
		storageB:   prometheus.NewDesc("tenantflow_tenant_storage_bytes", "Live database size in bytes.", labels, nil),
		workflowCt: prometheus.NewDesc("tenantflow_tenant_workflow_executions_30d", "Workflow executions started in the last 30 days.", labels, nil),
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.total
	ch <- c.storage
	ch <- c.compute
	ch <- c.memory
	ch <- c.workflows
	ch <- c.backups
	ch <- c.storageB
	ch <- c.workflowCt
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	estimates, err := c.loader(ctx)
	if err != nil {
		c.log.Error("cost collector: load estimates", "error", err)
		return
	}

	for _, te := range estimates {
		ch <- prometheus.MustNewConstMetric(c.total, prometheus.GaugeValue, te.Estimate.Total, te.TenantID)
		ch <- prometheus.MustNewConstMetric(c.storage, prometheus.GaugeValue, te.Estimate.Storage, te.TenantID)
		ch <- prometheus.MustNewConstMetric(c.compute, prometheus.GaugeValue, te.Estimate.Compute, te.TenantID)
		ch <- prometheus.MustNewConstMetric(c.memory, prometheus.GaugeValue, te.Estimate.Memory, te.TenantID)
		ch <- prometheus.MustNewConstMetric(c.workflows, prometheus.GaugeValue, te.Estimate.Workflows, te.TenantID)
		ch <- prometheus.MustNewConstMetric(c.backups, prometheus.GaugeValue, te.Estimate.Backups, te.TenantID)
		ch <- prometheus.MustNewConstMetric(c.storageB, prometheus.GaugeValue, float64(te.Resources.StorageBytes), te.TenantID)
		ch <- prometheus.MustNewConstMetric(c.workflowCt, prometheus.GaugeValue, float64(te.Resources.WorkflowExecutions), te.TenantID)
	}
}
