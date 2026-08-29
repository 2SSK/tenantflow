package workflow

import (
	"errors"
	"testing"

	"github.com/2SSK/tenantflow/internal/activities"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

func TestMigrateWorkflow_Success(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	// Registering the real activities lets the test-harness execute any
	// activity that is not explicitly mocked. NewMigrateActivities takes only
	// (auditRepo, provider); both nil is fine because every activity we care
	// about is mocked below, so the real bodies never run.
	env.RegisterActivity(activities.NewMigrateActivities(nil, nil))

	env.OnActivity(activities.MarkTenantMigratingActivityName, mock.Anything, "acme-mig").Return(nil)
	env.OnActivity(activities.MigrateDataActivityName, mock.Anything, "acme-mig").Return("acme-mig_20260101.sql", nil)
	env.OnActivity(activities.SwitchTrafficActivityName, mock.Anything, "acme-mig", "acme-mig_20260101.sql").Return(nil)
	env.OnActivity(activities.MarkTenantMigratedActivityName, mock.Anything, "acme-mig").Return(nil)

	env.ExecuteWorkflow(MigrateTenantWorkflow, MigrateInput{TenantID: "acme-mig"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	env.AssertCalled(t, activities.MarkTenantMigratingActivityName, mock.Anything, "acme-mig")
	env.AssertCalled(t, activities.MigrateDataActivityName, mock.Anything, "acme-mig")
	env.AssertCalled(t, activities.SwitchTrafficActivityName, mock.Anything, "acme-mig", "acme-mig_20260101.sql")
	env.AssertCalled(t, activities.MarkTenantMigratedActivityName, mock.Anything, "acme-mig")
	// Compensation must NOT run on success.
	env.AssertNotCalled(t, activities.DropTenantAuxDatabaseActivityName, mock.Anything, "acme-mig")
	env.AssertNotCalled(t, activities.MarkTenantMigrateFailedActivityName, mock.Anything, "acme-mig")
}

// Failure before the switch: the live DB was never replaced, so compensation
// must discard the disposable _new DB and mark the migration failed.
func TestMigrateWorkflow_FailBeforeSwitchDropsNewDB(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewMigrateActivities(nil, nil))

	env.OnActivity(activities.MarkTenantMigratingActivityName, mock.Anything, "acme-mig2").Return(nil)
	env.OnActivity(activities.MigrateDataActivityName, mock.Anything, "acme-mig2").Return(
		"", errors.New("snapshot failed: connection refused"))
	env.OnActivity(activities.DropTenantAuxDatabaseActivityName, mock.Anything, "acme-mig2").Return(nil)
	env.OnActivity(activities.MarkTenantMigrateFailedActivityName, mock.Anything, "acme-mig2").Return(nil)

	env.ExecuteWorkflow(MigrateTenantWorkflow, MigrateInput{TenantID: "acme-mig2"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("expected workflow error, got nil")
	}

	env.AssertCalled(t, activities.DropTenantAuxDatabaseActivityName, mock.Anything, "acme-mig2")
	env.AssertCalled(t, activities.MarkTenantMigrateFailedActivityName, mock.Anything, "acme-mig2")
	env.AssertNotCalled(t, activities.SwitchTrafficActivityName, mock.Anything, "acme-mig2", mock.Anything)
	env.AssertNotCalled(t, activities.MarkTenantMigratedActivityName, mock.Anything, "acme-mig2")
}

// Failure after the switch (switched=true): the _new DB has already been
// promoted into the live name, so compensation must NOT drop it (that would
// destroy live data). Only the failure audit event is written.
func TestMigrateWorkflow_FailAfterSwitchDoesNotDrop(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewMigrateActivities(nil, nil))

	const backup = "acme-mig3_20260101.sql"
	env.OnActivity(activities.MarkTenantMigratingActivityName, mock.Anything, "acme-mig3").Return(nil)
	env.OnActivity(activities.MigrateDataActivityName, mock.Anything, "acme-mig3").Return(backup, nil)
	env.OnActivity(activities.SwitchTrafficActivityName, mock.Anything, "acme-mig3", backup).Return(nil)
	// The workflow's last step fails after the switch completed.
	env.OnActivity(activities.MarkTenantMigratedActivityName, mock.Anything, "acme-mig3").Return(errors.New("audit write failed"))
	env.OnActivity(activities.MarkTenantMigrateFailedActivityName, mock.Anything, "acme-mig3").Return(nil)

	env.ExecuteWorkflow(MigrateTenantWorkflow, MigrateInput{TenantID: "acme-mig3"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("expected workflow error, got nil")
	}

	env.AssertCalled(t, activities.MarkTenantMigrateFailedActivityName, mock.Anything, "acme-mig3")
	// The _new DB was renamed into the live name, so dropping the aux DB would
	// be wrong. Assert it was NOT called.
	env.AssertNotCalled(t, activities.DropTenantAuxDatabaseActivityName, mock.Anything, "acme-mig3")
}
