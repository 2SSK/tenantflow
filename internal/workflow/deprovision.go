package workflow

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/2SSK/tenantflow/internal/activities"
)

type DeprovisionInput struct {
	TenantID string
}

func DeprovisionTenantWorkflow(ctx workflow.Context, in DeprovisionInput) error {
	logger := workflow.GetLogger(ctx)
	logger.Info("deprovision workflow started", "tenantID", in.TenantID)

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})

	if err := workflow.ExecuteActivity(actCtx, activities.MarkTenantDeletingActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	if err := workflow.ExecuteActivity(actCtx, activities.DeprovisionTenantActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	if err := workflow.ExecuteActivity(actCtx, activities.MarkTenantDeletedActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	logger.Info("deprovision workflow completed", "tenantID", in.TenantID)

	return nil
}
