package workflow

import (
	"errors"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/2SSK/tenantflow/internal/activities"
)

type RestoreInput struct {
	TenantID string
	BackupID int64
}

// RestoreTenantWorkflow overwrites a tenant's live database with the contents
// of a specific historical backup (referenced by ID), then validates the
// result. The tenant stays active throughout.
//
// Compensation strategy (why the `preSnapshotTaken` flag exists):
//   - Before the actual restore we snapshot the CURRENT live DB as a rollback
//     point. Once we have that snapshot, any failure rolls the live DB back to
//     it, guaranteeing the tenant never ends up on a half-restored / corrupt
//     state. If we failed before taking the snapshot (e.g. the backup record
//     didn't exist), there is nothing to roll back to and the live DB was never
//     touched.
//
// The rollback is safe to run even when RestoreData failed before mutating the
// DB: restoring the pre-snapshot simply re-writes identical data.
func RestoreTenantWorkflow(ctx workflow.Context, in RestoreInput) (err error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("Starting RestoreTenantWorkflow", "TenantID", in.TenantID, "BackupID", in.BackupID)

	info := workflow.GetInfo(ctx)
	workflowID := info.WorkflowExecution.ID

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})

	var preSnapshotTaken bool
	var preBackupName string

	defer func() {
		if err != nil {
			if preSnapshotTaken {
				rollbackErr := workflow.ExecuteActivity(actCtx, activities.RestoreRollbackActivityName, in.TenantID, preBackupName).Get(actCtx, nil)
				err = errors.Join(err, rollbackErr)
			}
			failErr := workflow.ExecuteActivity(actCtx, activities.MarkTenantRestoreFailedActivityName, in.TenantID).Get(actCtx, nil)
			err = errors.Join(err, failErr)
		}
	}()

	if err = workflow.ExecuteActivity(actCtx, activities.MarkTenantRestoringActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	if err = workflow.ExecuteActivity(actCtx, activities.PreRestoreSnapshotActivityName, in.TenantID).Get(actCtx, &preBackupName); err != nil {
		return err
	}
	preSnapshotTaken = true

	if err = workflow.ExecuteActivity(actCtx, activities.RestoreDataActivityName, in.TenantID, in.BackupID).Get(actCtx, nil); err != nil {
		return err
	}

	if err = workflow.ExecuteActivity(actCtx, activities.MarkTenantRestoredActivityName, in.TenantID, in.BackupID).Get(actCtx, nil); err != nil {
		return err
	}

	logger.Info("RestoreTenantWorkflow completed successfully", "TenantID", in.TenantID, "WorkflowID", workflowID, "BackupID", in.BackupID)
	return nil
}
