package workflow

import (
	"testing"

	"github.com/2SSK/tenantflow/internal/activities"
	"github.com/2SSK/tenantflow/internal/model"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

func activeReconcileSpec() model.TenantSpec {
	return model.TenantSpec{
		TenantID:        "acme-rec",
		Status:          model.TenantStatusActive,
		IsolationMode:   model.IsolationModeDedicated,
		RequireDatabase: true,
		RequireBackup:   true,
	}
}

// ReconcileActualState is healthy: database owned by the tenant role, no
// PUBLIC CONNECT, owner role present, one completed backup.
func healthyActualState() model.ReconcileActualState {
	return model.ReconcileActualState{
		TenantID: "acme-rec",
		Database: model.DatabaseActualState{
			Exists:        true,
			OwnerRole:     "tenant_acme-rec",
			PublicConnect: false,
		},
		RoleExists:       true,
		CompletedBackups: 1,
	}
}

// Already-converged fast path: no drift found, no repair, no failure audit.
func TestReconcileWorkflow_ConvergedWhenNoDrift(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	// Real activities registered so the harness can execute anything not
	// mocked; all deps nil because every activity below is mocked anyway.
	// BackupTenantData lives in BackupActivities, so register that type too.
	env.RegisterActivity(activities.NewReconcileActivities(nil, nil, nil, nil, nil))
	env.RegisterActivity(activities.NewBackupActivities(nil, nil, nil))

	env.OnActivity(activities.ResolveTenantSpecActivityName, mock.Anything, "acme-rec").Return(activeReconcileSpec(), nil)
	env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).Return(healthyActualState(), nil)
	env.OnActivity(activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec", []string{}).Return(nil)

	env.ExecuteWorkflow(ReconcileTenantWorkflow, ReconcileInput{TenantID: "acme-rec"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	env.AssertCalled(t, activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec", []string{})
	env.AssertNotCalled(t, activities.RecordReconcileDriftActivityName, mock.Anything, "acme-rec", mock.Anything)
	env.AssertNotCalled(t, activities.EnsureTenantDatabaseActivityName, mock.Anything, "acme-rec")
	env.AssertNotCalled(t, activities.BackupTenantDataActivityName, mock.Anything, "acme-rec")
	env.AssertNotCalled(t, activities.MarkReconcileFailedActivityName, mock.Anything, "acme-rec", mock.Anything)
}

// Repair path: probe #1 finds the only drift, repair runs (database action +
// backup), probe #2 proves convergence, and the run is reported as repaired.
func TestReconcileWorkflow_RepairsDriftAndConverges(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewReconcileActivities(nil, nil, nil, nil, nil))
	// Repair may fall back to BackupTenantData (BackupActivities), register it.
	env.RegisterActivity(activities.NewBackupActivities(nil, nil, nil))

	drifted := model.ReconcileActualState{
		TenantID:         "acme-rec",
		Database:         model.DatabaseActualState{Exists: false},
		RoleExists:       false,
		CompletedBackups: 0,
	}

	env.OnActivity(activities.ResolveTenantSpecActivityName, mock.Anything, "acme-rec").Return(activeReconcileSpec(), nil)
	// Probe is called twice: once before repair (drift), once after (clean).
	env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).Return(drifted, nil).Once()
	env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).Return(healthyActualState(), nil).Once()
	env.OnActivity(activities.RecordReconcileDriftActivityName, mock.Anything, "acme-rec",
		[]string{model.DriftMissingDatabase, model.DriftMissingRole, model.DriftMissingBackup}).Return(nil)
	env.OnActivity(activities.EnsureTenantDatabaseActivityName, mock.Anything, "acme-rec").Return(nil)
	env.OnActivity(activities.BackupTenantDataActivityName, mock.Anything, "acme-rec").Return(&model.Backup{TenantID: "acme-rec"}, nil)
	env.OnActivity(activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec",
		[]string{model.DriftMissingDatabase, model.DriftMissingRole, model.DriftMissingBackup}).Return(nil)

	env.ExecuteWorkflow(ReconcileTenantWorkflow, ReconcileInput{TenantID: "acme-rec"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	env.AssertCalled(t, activities.RecordReconcileDriftActivityName, mock.Anything, "acme-rec", mock.Anything)
	env.AssertCalled(t, activities.EnsureTenantDatabaseActivityName, mock.Anything, "acme-rec")
	env.AssertCalled(t, activities.BackupTenantDataActivityName, mock.Anything, "acme-rec")
	env.AssertCalled(t, activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec", mock.Anything)
	env.AssertCalled(t, activities.BackupTenantDataActivityName, mock.Anything, "acme-rec")
	env.AssertNotCalled(t, activities.MarkReconcileFailedActivityName, mock.Anything, "acme-rec", mock.Anything)
}

// Unconverged: repair claims success but the re-probe still shows drift.
// The run must fail (lands in the DLQ view) and audit the failure — never
// claim convergence.
func TestReconcileWorkflow_UnconvergedAfterRepairFails(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewReconcileActivities(nil, nil, nil, nil, nil))

	stillDrifted := model.ReconcileActualState{
		TenantID: "acme-rec",
		Database: model.DatabaseActualState{
			Exists:        true,
			OwnerRole:     "someone_else", // ownership drift survives repair
			PublicConnect: true,
		},
		RoleExists:       true,
		CompletedBackups: 1,
	}

	env.OnActivity(activities.ResolveTenantSpecActivityName, mock.Anything, "acme-rec").Return(activeReconcileSpec(), nil)
	env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).Return(stillDrifted, nil).Once()
	env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).Return(stillDrifted, nil).Once()
	env.OnActivity(activities.RecordReconcileDriftActivityName, mock.Anything, "acme-rec",
		[]string{model.DriftWrongOwner, model.DriftPublicConnect}).Return(nil)
	env.OnActivity(activities.EnsureTenantDatabaseActivityName, mock.Anything, "acme-rec").Return(nil)
	env.OnActivity(activities.MarkReconcileFailedActivityName, mock.Anything, "acme-rec", mock.Anything).Return(nil)

	env.ExecuteWorkflow(ReconcileTenantWorkflow, ReconcileInput{TenantID: "acme-rec"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("expected workflow error for unconverged run, got nil")
	}

	env.AssertCalled(t, activities.MarkReconcileFailedActivityName, mock.Anything, "acme-rec", mock.Anything)
	// No backup was drifted, so no backup repair; and it never converged.
	env.AssertNotCalled(t, activities.BackupTenantDataActivityName, mock.Anything, "acme-rec")
	env.AssertNotCalled(t, activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec", mock.Anything)
}

// Non-active tenants converge by construction: nothing was ever promised, so
// the run is recorded as skipped and no probe or repair happens.
func TestReconcileWorkflow_SkipsNonActiveTenant(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewReconcileActivities(nil, nil, nil, nil, nil))

	failedSpec := activeReconcileSpec()
	failedSpec.Status = model.TenantStatusFailed

	env.OnActivity(activities.ResolveTenantSpecActivityName, mock.Anything, "acme-rec").Return(failedSpec, nil)
	env.OnActivity(activities.MarkReconcileSkippedActivityName, mock.Anything, "acme-rec", mock.Anything).Return(nil)

	env.ExecuteWorkflow(ReconcileTenantWorkflow, ReconcileInput{TenantID: "acme-rec"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	env.AssertCalled(t, activities.MarkReconcileSkippedActivityName, mock.Anything, "acme-rec", mock.Anything)
	env.AssertNotCalled(t, activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything)
	env.AssertNotCalled(t, activities.EnsureTenantDatabaseActivityName, mock.Anything, "acme-rec")
	env.AssertNotCalled(t, activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec", mock.Anything)
}

// Shared-mode tenants promise no per-tenant infrastructure, so an active
// shared tenant converges trivially without any database probe.
func TestReconcileWorkflow_SharedTenantConvergesWithoutProbe(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewReconcileActivities(nil, nil, nil, nil, nil))

	sharedSpec := model.TenantSpec{
		TenantID:        "acme-rec",
		Status:          model.TenantStatusActive,
		IsolationMode:   model.IsolationModeShared,
		RequireDatabase: false,
		RequireBackup:   false,
	}

	env.OnActivity(activities.ResolveTenantSpecActivityName, mock.Anything, "acme-rec").Return(sharedSpec, nil)
	env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).Return(model.ReconcileActualState{TenantID: "acme-rec"}, nil)
	env.OnActivity(activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec", []string{}).Return(nil)

	env.ExecuteWorkflow(ReconcileTenantWorkflow, ReconcileInput{TenantID: "acme-rec"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	env.AssertCalled(t, activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec", []string{})
	env.AssertNotCalled(t, activities.RecordReconcileDriftActivityName, mock.Anything, "acme-rec", mock.Anything)
	env.AssertNotCalled(t, activities.EnsureTenantDatabaseActivityName, mock.Anything, "acme-rec")
}
