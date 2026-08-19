package workflow

import (
	"errors"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/2SSK/tenantflow/internal/activities"
)

const TaskQueue = "tenantflow-provision"

type ProvisionInput struct {
	TenantID string
}

func ProvisionTenantWorkflow(ctx workflow.Context, in ProvisionInput) (err error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("provision workflow started", "tenantID", in.TenantID)

	info := workflow.GetInfo(ctx)
	workflowID := info.WorkflowExecution.ID

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})

	// persist the tenant row as "provisioning" so the API
	// can report progress immediately.
	if err = workflow.ExecuteActivity(actCtx, activities.CreateTenantRecordActivityName, in.TenantID, workflowID).Get(actCtx, nil); err != nil {
		return err
	}

	defer func() {
		if err != nil {
			dropErr := workflow.ExecuteActivity(actCtx, activities.DropTenantDatabaseActivityName, in.TenantID).Get(actCtx, nil)
			failErr := workflow.ExecuteActivity(actCtx, activities.MarkTenantFailedActivityName, in.TenantID).Get(actCtx, nil)
			err = errors.Join(err, dropErr, failErr)
		}
	}()

	// do the real provisioning work
	if err = workflow.ExecuteActivity(actCtx, activities.ProvisionTenantActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	// flip the tenant to "active"
	if err = workflow.ExecuteActivity(actCtx, activities.MarkTenantActiveActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	logger.Info("provision workflow completed", "tenantID", in.TenantID)

	return nil
}
