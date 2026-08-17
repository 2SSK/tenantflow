package workflow

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/mock"

	"go.temporal.io/sdk/testsuite"

	"github.com/2SSK/tenantflow/internal/activities"
)

func TestProvisionWorkflow_SuccessDoesNotCompensate(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewProvisionActivities(nil, nil))

	env.OnActivity(activities.CreateTenantRecordActivityName, mock.Anything, "acme-ok", mock.Anything).Return(nil)
	env.OnActivity(activities.ProvisionTenantActivityName, mock.Anything, "acme-ok").Return(nil)
	env.OnActivity(activities.MarkTenantActiveActivityName, mock.Anything, "acme-ok").Return(nil)

	env.ExecuteWorkflow(ProvisionTenantWorkflow, ProvisionInput{TenantID: "acme-ok"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	env.AssertNotCalled(t, activities.MarkTenantFailedActivityName, mock.Anything, "acme-ok")
}

func TestProvisionWorkflow_FailureCompensates(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewProvisionActivities(nil, nil))

	env.OnActivity(activities.CreateTenantRecordActivityName, mock.Anything, "fail-tenant", mock.Anything).Return(nil)
	env.OnActivity(activities.ProvisionTenantActivityName, mock.Anything, "fail-tenant").Return(fmt.Errorf("simulated provisioning failure"))
	env.OnActivity(activities.MarkTenantFailedActivityName, mock.Anything, "fail-tenant").Return(nil)
	env.ExecuteWorkflow(ProvisionTenantWorkflow, ProvisionInput{
		TenantID: "fail-tenant",
	})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("expected workflow error, got nil")
	}

	env.AssertCalled(t, activities.MarkTenantFailedActivityName, mock.Anything, "fail-tenant")
	env.AssertNotCalled(t, activities.MarkTenantActiveActivityName, mock.Anything, "fail-tenant")
}
