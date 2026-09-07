package cost

import (
	"math"
	"testing"
)

func TestMonthlyEstimate(t *testing.T) {
	// A dedicated tenant with a ~1 GB database, 10 runs, no backups.
	r := Resources{
		StorageBytes:       1024 * 1024 * 1024,
		IsolationMode:      "dedicated",
		WorkflowExecutions: 10,
		BackupCount:        0,
	}
	got := MonthlyEstimate(r, DefaultModel)

	// Storage: 1 GB * $0.10 = 0.10
	if math.Abs(got.Storage-0.10) > 1e-9 {
		t.Errorf("storage = %v, want 0.10", got.Storage)
	}
	// Compute: 1 vCPU * $0.02 * 730h = 14.60
	if math.Abs(got.Compute-14.60) > 1e-9 {
		t.Errorf("compute = %v, want 14.60", got.Compute)
	}
	// Memory: 2 GB * $0.002 * 730h = 2.92
	if math.Abs(got.Memory-2.92) > 1e-9 {
		t.Errorf("memory = %v, want 2.92", got.Memory)
	}
	// Workflows: 10 * $0.0001 = 0.001
	if math.Abs(got.Workflows-0.001) > 1e-9 {
		t.Errorf("workflows = %v, want 0.001", got.Workflows)
	}
	if math.Abs(got.Total-17.621) > 1e-9 {
		t.Errorf("total = %v, want 17.621", got.Total)
	}
}

func TestSpecForIsolation(t *testing.T) {
	if got := SpecForIsolation("shared"); got.VCPUs != 0.25 || got.MemoryGB != 0.5 {
		t.Errorf("shared spec = %+v, want 0.25/0.5", got)
	}
	if got := SpecForIsolation("dedicated"); got.VCPUs != 1 || got.MemoryGB != 2 {
		t.Errorf("dedicated spec = %+v, want 1/2", got)
	}
}

func TestBackupStorageBilled(t *testing.T) {
	base := Resources{StorageBytes: 1024 * 1024 * 1024, IsolationMode: "dedicated", BackupCount: 0}
	withBackup := base
	withBackup.BackupCount = 1

	got := MonthlyEstimate(withBackup, DefaultModel)
	// Live storage is a separate line item from backups; the backup appears
	// as its own "backups" charge of one ~1 GB snapshot (0.10).
	if math.Abs(got.Storage-0.10) > 1e-9 {
		t.Errorf("storage = %v, want 0.10 (unchanged by backups)", got.Storage)
	}
	if math.Abs(got.Backups-0.10) > 1e-9 {
		t.Errorf("backups = %v, want 0.10", got.Backups)
	}
	// One backup adds exactly one storage's worth to the total.
	if math.Abs(got.Total-MonthlyEstimate(base, DefaultModel).Total-0.10) > 1e-9 {
		t.Errorf("total delta with backup = %v, want 0.10", got.Total-MonthlyEstimate(base, DefaultModel).Total)
	}
}
