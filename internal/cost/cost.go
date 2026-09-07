// Package cost estimates the simulated monthly cost of a tenant from
// resource metadata, the way real SaaS billing systems work; the price
// model is a published rate card applied to measured usage.
package cost

// PriceModel is the simulated rate card. Units make the model readable:
// rates are per GB-month, per vCPU-hour, per GB-hour, and per workflow run.
type PriceModel struct {
	StoragePerGBMonth  float64 // dollars per GB of stored database per month
	ComputePerVCPUHour float64 // dollars per vCPU per hour
	MemoryPerGBHour    float64 // dollars per GB of RAM per hour
	PerWorkflowRun     float64 // dollars per workflow execution
}

// HoursPerMonth is the billing convention: one month = 730 hours.
const HoursPerMonth = 730

// DefaultModel is the published rate card for the simulated cloud.
var DefaultModel = PriceModel{
	StoragePerGBMonth:  0.10,
	ComputePerVCPUHour: 0.02,
	MemoryPerGBHour:    0.002,
	PerWorkflowRun:     0.0001,
}

// Spec is the container a tenant would run in, derived from its isolation
// tier. A dedicated tenant gets a full sized container; shared tenants get
// a fraction. The spec is a published fact of the simulated cloud.
type Spec struct {
	VCPUs    float64
	MemoryGB float64
}

// SpecForIsolation maps the two tiers the platform supports to container
// specs. Keep in sync with model.IsolationMode.
func SpecForIsolation(isolationMode string) Spec {
	switch isolationMode {
	case "shared":
		return Spec{VCPUs: 0.25, MemoryGB: 0.5}
	default: // dedicated and anything unset
		return Spec{VCPUs: 1, MemoryGB: 2}
	}
}

// Resources are the measured metadata for one tenant over one month.
type Resources struct {
	StorageBytes       int64  `json:"storageBytes"`       // live database size, from pg_database_size
	IsolationMode      string `json:"isolationMode"`      // "dedicated" | "shared"
	WorkflowExecutions int    `json:"workflowExecutions"` // runs in the last 30 days
	BackupCount        int    `json:"backupCount"`        // snapshots currently retained
}

// TenantResources pairs a tenant with its measured metadata. The repository
// returns these for the Prometheus collector; the estimate is computed at
// the caller, keeping the pricing rule out of the measurement layer.
type TenantResources struct {
	TenantID  string    `json:"tenantID"`
	Resources Resources `json:"resources"`
}

// Estimate is the monthly cost breakdown, exposed on the cost endpoint so
// operators can see WHERE the money goes, not just the total.
type Estimate struct {
	Storage   float64 `json:"storage"`
	Compute   float64 `json:"compute"`
	Memory    float64 `json:"memory"`
	Workflows float64 `json:"workflows"`
	Backups   float64 `json:"backups"`
	Total     float64 `json:"total"`
}

// MonthlyEstimate converts measured resources into dollars. Every unit
// conversion happens here, in one place; callers never do GB math.
func MonthlyEstimate(r Resources, m PriceModel) Estimate {
	storageGB := float64(r.StorageBytes) / (1024 * 1024 * 1024)

	// Compute + memory are billed per hour, for the whole month.
	spec := SpecForIsolation(r.IsolationMode)

	// Each retained backup is treated as a snapshot roughly the size of
	// the current database.
	backupGB := storageGB * float64(r.BackupCount)

	e := Estimate{
		Storage:   storageGB * m.StoragePerGBMonth,
		Compute:   spec.VCPUs * m.ComputePerVCPUHour * HoursPerMonth,
		Memory:    spec.MemoryGB * m.MemoryPerGBHour * HoursPerMonth,
		Workflows: float64(r.WorkflowExecutions) * m.PerWorkflowRun,
		Backups:   backupGB * m.StoragePerGBMonth,
	}
	e.Total = e.Storage + e.Compute + e.Memory + e.Workflows + e.Backups
	return e
}
