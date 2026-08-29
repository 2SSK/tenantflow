package workflow

import (
	"context"
	"fmt"
	"testing"

	"github.com/2SSK/tenantflow/internal/activities"
	"github.com/2SSK/tenantflow/internal/billing"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

func TestUpgradeWorkflow_SuccessDoesNotRollback(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewUpgradeActivities(nil, nil, nil))

	oldQ := billing.Quota{MaxUsers: 10, MaxStorageGB: 5, MaxSeats: 10}

	env.OnActivity(activities.MarkTenantUpgradingActivityName, mock.Anything, "acme-up").Return(nil)
	env.OnActivity(activities.VerifyTenantActiveActivityName, mock.Anything, "acme-up").Return(oldQ, nil)
	env.OnActivity(activities.RaiseQuotasActivityName, mock.Anything, "acme-up", mock.Anything).Return(
		func(_ context.Context, _ string, old billing.Quota) (billing.Quota, error) {
			return billing.Quota{MaxUsers: old.MaxUsers * 2, MaxStorageGB: old.MaxStorageGB * 4, MaxSeats: old.MaxSeats * 2}, nil
		})
	env.OnActivity(activities.EnableFeaturesActivityName, mock.Anything, "acme-up", mock.Anything).Return(nil)
	env.OnActivity(activities.UpdateBillingActivityName, mock.Anything, "acme-up", mock.Anything).Return(nil)
	env.OnActivity(activities.MarkTenantUpgradedActivityName, mock.Anything, "acme-up").Return(nil)

	env.ExecuteWorkflow(UpgradeTenantWorkflow, UpgradeInput{TenantID: "acme-up"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	env.AssertNotCalled(t, activities.RollbackQuotasActivityName, mock.Anything, "acme-up", mock.Anything)
	env.AssertCalled(t, activities.MarkTenantUpgradedActivityName, mock.Anything, "acme-up")
}

func TestUpgradeWorkflow_FailureRollsBackQuotas(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewUpgradeActivities(nil, nil, nil))

	oldQ := billing.Quota{MaxUsers: 10, MaxStorageGB: 5, MaxSeats: 10}

	env.OnActivity(activities.MarkTenantUpgradingActivityName, mock.Anything, "acme-dn").Return(nil)
	env.OnActivity(activities.VerifyTenantActiveActivityName, mock.Anything, "acme-dn").Return(oldQ, nil)
	env.OnActivity(activities.RaiseQuotasActivityName, mock.Anything, "acme-dn", mock.Anything).Return(
		func(_ context.Context, _ string, old billing.Quota) (billing.Quota, error) {
			return billing.Quota{MaxUsers: old.MaxUsers * 2, MaxStorageGB: old.MaxStorageGB * 4, MaxSeats: old.MaxSeats * 2}, nil
		})
	env.OnActivity(activities.EnableFeaturesActivityName, mock.Anything, "acme-dn", mock.Anything).Return(fmt.Errorf("feature flag service down"))
	env.OnActivity(activities.RollbackQuotasActivityName, mock.Anything, "acme-dn", mock.Anything).Return(nil)
	env.OnActivity(activities.MarkTenantUpgradeFailedActivityName, mock.Anything, "acme-dn").Return(nil)

	env.ExecuteWorkflow(UpgradeTenantWorkflow, UpgradeInput{TenantID: "acme-dn"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("expected workflow error, got nil")
	}

	env.AssertCalled(t, activities.RollbackQuotasActivityName, mock.Anything, "acme-dn", oldQ)
	env.AssertCalled(t, activities.MarkTenantUpgradeFailedActivityName, mock.Anything, "acme-dn")
	env.AssertNotCalled(t, activities.MarkTenantUpgradedActivityName, mock.Anything, "acme-dn")
}
