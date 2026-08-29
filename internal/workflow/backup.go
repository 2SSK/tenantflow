package workflow

import (
	"errors"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/2SSK/tenantflow/internal/activities"
	"github.com/2SSK/tenantflow/internal/model"
)

type BackupInput struct {
	TenantID string
}

// BackupTenantWorkflow takes a verified point-in-time backup of a tenant's
// database. Unlike the migration saga there is no `switched`-style guard flag:
// backup never mutates the live DB, so the only compensation on failure is
// recording that the backup did not complete on the audit timeline. Any backup
// record created mid-flight is marked failed inside BackupTenantData itself.
func BackupTenantWorkflow(ctx workflow.Context, in BackupInput) (err error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("Starting BackupTenantWorkflow", "TenantID", in.TenantID)

	info := workflow.GetInfo(ctx)
	workflowID := info.WorkflowExecution.ID

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})

	defer func() {
		if err != nil {
			failErr := workflow.ExecuteActivity(actCtx, activities.MarkTenantBackupFailedActivityName, in.TenantID).Get(actCtx, nil)
			err = errors.Join(err, failErr)
		}
	}()

	if err = workflow.ExecuteActivity(actCtx, activities.MarkTenantBackingUpActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	var rec *model.Backup
	if err = workflow.ExecuteActivity(actCtx, activities.BackupTenantDataActivityName, in.TenantID).Get(actCtx, &rec); err != nil {
		return err
	}

	if err = workflow.ExecuteActivity(actCtx, activities.MarkTenantBackedUpActivityName, in.TenantID, rec.ID, rec.Filename).Get(actCtx, nil); err != nil {
		return err
	}

	logger.Info("BackupTenantWorkflow completed successfully", "TenantID", in.TenantID, "WorkflowID", workflowID, "BackupID", rec.ID)
	return nil
}
