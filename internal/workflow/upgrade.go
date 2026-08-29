package workflow

import (
	"errors"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/2SSK/tenantflow/internal/activities"
	"github.com/2SSK/tenantflow/internal/billing"
)

type UpgradeInput struct {
	TenantID string
}

func UpgradeTenantWorkflow(ctx workflow.Context, in UpgradeInput) (err error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("Starting UpgradeTenantWorkflow", "TenantID", in.TenantID)

	info := workflow.GetInfo(ctx)
	workflowID := info.WorkflowExecution.ID

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})

	var oldQuota billing.Quota
	var quotaRaised bool

	defer func() {
		if err != nil {
			if quotaRaised {
				rollbackErr := workflow.ExecuteActivity(actCtx, activities.RollbackQuotasActivityName, in.TenantID, oldQuota).Get(actCtx, nil)
				err = errors.Join(err, rollbackErr)
			}
			failErr := workflow.ExecuteActivity(actCtx, activities.MarkTenantUpgradeFailedActivityName, in.TenantID).Get(actCtx, nil)
			err = errors.Join(err, failErr)
		}
	}()

	if err = workflow.ExecuteActivity(actCtx, activities.MarkTenantUpgradingActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	if err = workflow.ExecuteActivity(actCtx, activities.VerifyTenantActiveActivityName, in.TenantID).Get(actCtx, &oldQuota); err != nil {
		return err
	}

	var newQuota billing.Quota
	if err = workflow.ExecuteActivity(actCtx, activities.RaiseQuotasActivityName, in.TenantID, oldQuota).Get(actCtx, &newQuota); err != nil {
		return err
	}

	quotaRaised = true

	if err = workflow.ExecuteActivity(actCtx, activities.EnableFeaturesActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	if err = workflow.ExecuteActivity(actCtx, activities.UpdateBillingActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	if err = workflow.ExecuteActivity(actCtx, activities.MarkTenantUpgradedActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	logger.Info("UpgradeTenantWorkflow completed successfully", "TenantID", in.TenantID, "WorkflowID", workflowID)
	return nil
}
