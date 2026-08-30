package workflow

import (
	"errors"
	"testing"
	"time"

	"github.com/2SSK/tenantflow/internal/activities"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

// The grace-period timer expires without a cancel signal: the saga must
// proceed to teardown (deprovision + mark deleted) and must NOT run the
// restore/cancel path.
func TestDeleteWorkflow_TimerExpirySucceeds(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewDeprovisionActivities(nil, nil))
	env.RegisterActivity(activities.NewCancelDeleteActivities(nil, nil))

	env.OnActivity(activities.MarkTenantDeletingActivityName, mock.Anything, "acme-del").Return(nil)
	env.OnActivity(activities.DeprovisionTenantActivityName, mock.Anything, "acme-del").Return(nil)
	env.OnActivity(activities.MarkTenantDeletedActivityName, mock.Anything, "acme-del").Return(nil)

	env.ExecuteWorkflow(DeleteTenantWorkflow, DeleteInput{TenantID: "acme-del", GracePeriod: 30 * 24 * time.Hour})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	env.AssertCalled(t, activities.MarkTenantDeletingActivityName, mock.Anything, "acme-del")
	env.AssertCalled(t, activities.DeprovisionTenantActivityName, mock.Anything, "acme-del")
	env.AssertCalled(t, activities.MarkTenantDeletedActivityName, mock.Anything, "acme-del")
	// The cancel path must not run when the timer wins.
	env.AssertNotCalled(t, activities.RestoreTenantAfterCancelActivityName, mock.Anything, "acme-del")
	// No failure audit on a clean run.
	env.AssertNotCalled(t, activities.MarkTenantDeleteFailedActivityName, mock.Anything, "acme-del")
}

// The operator sends the cancel-delete signal DURING the grace period (before
// the timer expires): the saga must take the restore path (status back to
// active) and must NOT tear anything down.
func TestDeleteWorkflow_CancelSignalDuringGracePeriod(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewDeprovisionActivities(nil, nil))
	env.RegisterActivity(activities.NewCancelDeleteActivities(nil, nil))

	env.OnActivity(activities.MarkTenantDeletingActivityName, mock.Anything, "acme-del").Return(nil)
	env.OnActivity(activities.RestoreTenantAfterCancelActivityName, mock.Anything, "acme-del").Return(nil)

	// RegisteredDelayedCallback fires at virtual time 24h — well before the
	// 720h grace-period timer — so the selector must pick the signal branch.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(CancelDeleteSignalName, struct{}{})
	}, 24*time.Hour)

	env.ExecuteWorkflow(DeleteTenantWorkflow, DeleteInput{TenantID: "acme-del", GracePeriod: 30 * 24 * time.Hour})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	env.AssertCalled(t, activities.RestoreTenantAfterCancelActivityName, mock.Anything, "acme-del")
	// Teardown must never run once the delete was cancelled.
	env.AssertNotCalled(t, activities.DeprovisionTenantActivityName, mock.Anything, "acme-del")
	env.AssertNotCalled(t, activities.MarkTenantDeletedActivityName, mock.Anything, "acme-del")
	// A graceful cancel is not a failure.
	env.AssertNotCalled(t, activities.MarkTenantDeleteFailedActivityName, mock.Anything, "acme-del")
}

// Teardown fails AFTER the grace period expired: the tenant is left honestly
// in "deleting" (reviving a half-torn-down tenant would be a lie) and the
// TENANT_DELETE_FAILED audit event is written via the deferred compensation.
// Crucially, the cancel path must NOT run — a late signal does not retroactively
// "cancel" a deletion that already failed.
func TestDeleteWorkflow_TeardownFailsAfterTimerExpiry(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewDeprovisionActivities(nil, nil))
	env.RegisterActivity(activities.NewCancelDeleteActivities(nil, nil))

	env.OnActivity(activities.MarkTenantDeletingActivityName, mock.Anything, "acme-del").Return(nil)
	env.OnActivity(activities.DeprovisionTenantActivityName, mock.Anything, "acme-del").Return(errors.New("teardown boom: connection refused"))
	env.OnActivity(activities.MarkTenantDeleteFailedActivityName, mock.Anything, "acme-del").Return(nil)

	env.ExecuteWorkflow(DeleteTenantWorkflow, DeleteInput{TenantID: "acme-del", GracePeriod: 30 * 24 * time.Hour})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("expected workflow error, got nil")
	}

	env.AssertCalled(t, activities.MarkTenantDeleteFailedActivityName, mock.Anything, "acme-del")
	// Neither the restore path nor the success path may run.
	env.AssertNotCalled(t, activities.RestoreTenantAfterCancelActivityName, mock.Anything, "acme-del")
	env.AssertNotCalled(t, activities.MarkTenantDeletedActivityName, mock.Anything, "acme-del")
}

// The cancel signal arrives and the restore activity itself fails: the
// deferred compensation still audits TENANT_DELETE_FAILED and the workflow
// surfaces the error, so the stuck state is visible to operators.
func TestDeleteWorkflow_CancelRestoreFails(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewDeprovisionActivities(nil, nil))
	env.RegisterActivity(activities.NewCancelDeleteActivities(nil, nil))

	env.OnActivity(activities.MarkTenantDeletingActivityName, mock.Anything, "acme-del").Return(nil)
	env.OnActivity(activities.RestoreTenantAfterCancelActivityName, mock.Anything, "acme-del").Return(errors.New("restore failed: db unavailable"))
	env.OnActivity(activities.MarkTenantDeleteFailedActivityName, mock.Anything, "acme-del").Return(nil)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(CancelDeleteSignalName, struct{}{})
	}, 24*time.Hour)

	env.ExecuteWorkflow(DeleteTenantWorkflow, DeleteInput{TenantID: "acme-del", GracePeriod: 30 * 24 * time.Hour})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("expected workflow error, got nil")
	}

	env.AssertCalled(t, activities.MarkTenantDeleteFailedActivityName, mock.Anything, "acme-del")
	env.AssertNotCalled(t, activities.DeprovisionTenantActivityName, mock.Anything, "acme-del")
}
