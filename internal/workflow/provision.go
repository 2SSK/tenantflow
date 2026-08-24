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

	var userID string
	var dbCreated bool

	defer func() {
		if err != nil {
			if userID != "" {
				dropIdentityErr := workflow.ExecuteActivity(actCtx, activities.DeleteTenantIdentityActivityName, userID).Get(actCtx, nil)
				err = errors.Join(err, dropIdentityErr)
			}
			if dbCreated {
				dropDBErr := workflow.ExecuteActivity(actCtx, activities.DropTenantDatabaseActivityName, in.TenantID).Get(actCtx, nil)
				err = errors.Join(err, dropDBErr)
			}
			failErr := workflow.ExecuteActivity(actCtx, activities.MarkTenantFailedActivityName, in.TenantID).Get(actCtx, nil)
			err = errors.Join(err, failErr)
		}
	}()

	if err = workflow.ExecuteActivity(actCtx, activities.CreateTenantRecordActivityName, in.TenantID, workflowID).Get(actCtx, nil); err != nil {
		return err
	}

	if err = workflow.ExecuteActivity(actCtx, activities.CreateTenantRecordActivityName, in.TenantID, workflowID).Get(actCtx, nil); err != nil {
		return err
	}

	if err = workflow.ExecuteActivity(actCtx, activities.ProvisionTenantActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}
	dbCreated = true

	if err = workflow.ExecuteActivity(actCtx, activities.ProvisionTenantIdentityActivityName, in.TenantID).Get(actCtx, &userID); err != nil {
		return err
	}

	if err = workflow.ExecuteActivity(actCtx, activities.MarkTenantActiveActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	logger.Info("provision workflow completed", "tenantID", in.TenantID)
	return nil
}
