package workflow

import (
	"context"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const TaskQueue = "tenantflow-provision"

type ProvisionInput struct {
	TenantID string
}

func ProvisionTenantWorkflow(ctx workflow.Context, in ProvisionInput) (string, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("provision workflow started", "tenantID", in.TenantID)

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})

	var result string
	if err := workflow.ExecuteActivity(actCtx, ProvisionActivity, in.TenantID).Get(actCtx, &result); err != nil {
		return "", err
	}

	logger.Info("provision workflow completed", "tenantID", in.TenantID)

	return result, nil
}

func ProvisionActivity(ctx context.Context, tenantID string) (string, error) {
	activity.GetLogger(ctx).Info("provisioning tenant", "tenantID", tenantID)

	return "provisioned: " + tenantID, nil
}
