package workflow

import (
	"errors"
	"testing"

	"github.com/2SSK/tenantflow/internal/activities"
	"github.com/2SSK/tenantflow/internal/model"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

func TestBackupWorkflow_Success(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewBackupActivities(nil, nil, nil))

	rec := &model.Backup{
		ID:       42,
		TenantID: "acme-bk",
		Filename: "acme-bk_20260101.sql",
		Status:   model.BackupStatusCompleted,
	}
	env.OnActivity(activities.MarkTenantBackingUpActivityName, mock.Anything, "acme-bk").Return(nil)
	env.OnActivity(activities.BackupTenantDataActivityName, mock.Anything, "acme-bk").Return(rec, nil)
	env.OnActivity(activities.MarkTenantBackedUpActivityName, mock.Anything, "acme-bk", rec.ID, rec.Filename).Return(nil)

	env.ExecuteWorkflow(BackupTenantWorkflow, BackupInput{TenantID: "acme-bk"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	env.AssertCalled(t, activities.MarkTenantBackingUpActivityName, mock.Anything, "acme-bk")
	env.AssertCalled(t, activities.BackupTenantDataActivityName, mock.Anything, "acme-bk")
	env.AssertCalled(t, activities.MarkTenantBackedUpActivityName, mock.Anything, "acme-bk", rec.ID, rec.Filename)
	// Compensation must NOT run on success.
	env.AssertNotCalled(t, activities.MarkTenantBackupFailedActivityName, mock.Anything, "acme-bk")
}

// Failure in the data activity: the only compensation is recording the failed
// backup on the timeline (record-level failure is handled inside the activity).
func TestBackupWorkflow_FailureMarksFailed(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewBackupActivities(nil, nil, nil))

	env.OnActivity(activities.MarkTenantBackingUpActivityName, mock.Anything, "acme-bk2").Return(nil)
	env.OnActivity(activities.BackupTenantDataActivityName, mock.Anything, "acme-bk2").Return(
		(*model.Backup)(nil), errors.New("snapshot failed: connection refused"))
	env.OnActivity(activities.MarkTenantBackupFailedActivityName, mock.Anything, "acme-bk2").Return(nil)

	env.ExecuteWorkflow(BackupTenantWorkflow, BackupInput{TenantID: "acme-bk2"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("expected workflow error, got nil")
	}

	env.AssertCalled(t, activities.MarkTenantBackupFailedActivityName, mock.Anything, "acme-bk2")
	env.AssertNotCalled(t, activities.MarkTenantBackedUpActivityName, mock.Anything, "acme-bk2", mock.Anything, mock.Anything)
}
