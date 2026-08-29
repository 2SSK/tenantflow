package workflow

import (
	"errors"
	"testing"

	"github.com/2SSK/tenantflow/internal/activities"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

func TestRestoreWorkflow_Success(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewRestoreActivities(nil, nil, nil))

	const pre = "acme-rest_20260101.sql"
	env.OnActivity(activities.MarkTenantRestoringActivityName, mock.Anything, "acme-rest").Return(nil)
	env.OnActivity(activities.PreRestoreSnapshotActivityName, mock.Anything, "acme-rest").Return(pre, nil)
	env.OnActivity(activities.RestoreDataActivityName, mock.Anything, "acme-rest", int64(7)).Return(nil)
	env.OnActivity(activities.MarkTenantRestoredActivityName, mock.Anything, "acme-rest", int64(7)).Return(nil)

	env.ExecuteWorkflow(RestoreTenantWorkflow, RestoreInput{TenantID: "acme-rest", BackupID: 7})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	env.AssertCalled(t, activities.MarkTenantRestoringActivityName, mock.Anything, "acme-rest")
	env.AssertCalled(t, activities.PreRestoreSnapshotActivityName, mock.Anything, "acme-rest")
	env.AssertCalled(t, activities.RestoreDataActivityName, mock.Anything, "acme-rest", int64(7))
	env.AssertCalled(t, activities.MarkTenantRestoredActivityName, mock.Anything, "acme-rest", int64(7))
	// Compensation must NOT run on success.
	env.AssertNotCalled(t, activities.RestoreRollbackActivityName, mock.Anything, mock.Anything, mock.Anything)
	env.AssertNotCalled(t, activities.MarkTenantRestoreFailedActivityName, mock.Anything, "acme-rest")
}

// Failure before the pre-restore snapshot is taken: the live DB was never
// touched and there is no snapshot to roll back to, so compensation must NOT
// attempt a rollback - only the failure audit event is written.
func TestRestoreWorkflow_FailBeforeSnapshotNoRollback(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewRestoreActivities(nil, nil, nil))

	env.OnActivity(activities.MarkTenantRestoringActivityName, mock.Anything, "acme-rest2").Return(nil)
	env.OnActivity(activities.PreRestoreSnapshotActivityName, mock.Anything, "acme-rest2").Return(
		"", errors.New("snapshot failed: connection refused"))
	env.OnActivity(activities.MarkTenantRestoreFailedActivityName, mock.Anything, "acme-rest2").Return(nil)

	env.ExecuteWorkflow(RestoreTenantWorkflow, RestoreInput{TenantID: "acme-rest2", BackupID: 7})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("expected workflow error, got nil")
	}

	env.AssertCalled(t, activities.MarkTenantRestoreFailedActivityName, mock.Anything, "acme-rest2")
	// No snapshot was captured, so there is nothing to roll back to.
	env.AssertNotCalled(t, activities.RestoreRollbackActivityName, mock.Anything, mock.Anything, mock.Anything)
	env.AssertNotCalled(t, activities.RestoreDataActivityName, mock.Anything, mock.Anything, mock.Anything)
}

// Failure after RestoreData (the live DB was overwritten) but before the final
// audit: compensation MUST roll the live DB back to the pre-restore snapshot to
// guarantee the tenant is not left on partially-restored data, then mark failed.
func TestRestoreWorkflow_FailAfterRestoreRollsBack(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewRestoreActivities(nil, nil, nil))

	const pre = "acme-rest3_20260101.sql"
	env.OnActivity(activities.MarkTenantRestoringActivityName, mock.Anything, "acme-rest3").Return(nil)
	env.OnActivity(activities.PreRestoreSnapshotActivityName, mock.Anything, "acme-rest3").Return(pre, nil)
	env.OnActivity(activities.RestoreDataActivityName, mock.Anything, "acme-rest3", int64(7)).Return(nil)
	env.OnActivity(activities.MarkTenantRestoredActivityName, mock.Anything, "acme-rest3", int64(7)).Return(errors.New("audit write failed"))
	env.OnActivity(activities.RestoreRollbackActivityName, mock.Anything, "acme-rest3", pre).Return(nil)
	env.OnActivity(activities.MarkTenantRestoreFailedActivityName, mock.Anything, "acme-rest3").Return(nil)

	env.ExecuteWorkflow(RestoreTenantWorkflow, RestoreInput{TenantID: "acme-rest3", BackupID: 7})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("expected workflow error, got nil")
	}

	env.AssertCalled(t, activities.RestoreRollbackActivityName, mock.Anything, "acme-rest3", pre)
	env.AssertCalled(t, activities.MarkTenantRestoreFailedActivityName, mock.Anything, "acme-rest3")
}
