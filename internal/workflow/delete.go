package workflow

import (
	"errors"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/2SSK/tenantflow/internal/activities"
)

// CancelDeleteSignalName is the signal an operator sends to stop an in-flight
// deletion during the grace period. The handler (POST /cancel-delete) signals
// the running workflow by this name.
const CancelDeleteSignalName = "cancel-delete"

type DeleteInput struct {
	TenantID    string
	GracePeriod time.Duration
}

// DeleteTenantWorkflow implements soft delete with a durable grace period:
//
//	MarkTenantDeleting (status → "deleting")
//	        ↓
//	durable timer (GracePeriod) ─┐   cancel-delete signal ─┐
//	        ↓                    │        ↓               │
//	   timer expires            │   never: signal won   ──┤
//	        ↓                    │        │               │
//	DeprovisionTenant        ←──┘   RestoreTenantAfterCancel
//	        ↓                            (status → "active")
//	MarkTenantDeleted → "deleted"
//
// The power of Temporal: the 30-day wait is a *durable timer* recorded in the
// workflow history, so it survives worker crashes and restarts; and the cancel
// signal is a *named message* Temporal buffers and delivers even if the
// workflow is between tasks. The selector waits for whichever arrives first.
//
// Compensation: on the graceful cancel path we flip the tenant back to active.
// If a step fails (e.g. teardown), we only audit TENANT_DELETE_FAILED and leave
// the tenant in "deleting" — reviving a half-torn-down tenant would be a lie,
// and the operator can investigate the stuck state.
func DeleteTenantWorkflow(ctx workflow.Context, in DeleteInput) (err error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("Starting DeleteTenantWorkflow", "TenantID", in.TenantID, "GracePeriod", in.GracePeriod)

	info := workflow.GetInfo(ctx)
	workflowID := info.WorkflowExecution.ID

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})

	// cancelled is the saga's guard flag: once the operator's cancel signal
	// arrives, we take the restore path instead of the teardown path.
	var cancelled bool

	defer func() {
		if err != nil {
			failErr := workflow.ExecuteActivity(actCtx, activities.MarkTenantDeleteFailedActivityName, in.TenantID).Get(actCtx, nil)
			err = errors.Join(err, failErr)
		}
	}()

	if err = workflow.ExecuteActivity(actCtx, activities.MarkTenantDeletingActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	// ── Durable wait: whichever comes first, the timer or the signal. ──
	// The timer future is recorded in history (survives crashes). The signal
	// channel receives "cancel-delete" messages sent by the API server. The
	// selector blocks durably until exactly one of them is ready.
	timerFuture := workflow.NewTimer(ctx, in.GracePeriod)
	signalChan := workflow.GetSignalChannel(ctx, CancelDeleteSignalName)

	selector := workflow.NewSelector(ctx)
	selector.AddFuture(timerFuture, func(f workflow.Future) {
		// Timer expired: fall through to teardown.
		logger.Info("grace period expired, proceeding with teardown", "TenantID", in.TenantID)
	})
	selector.AddReceive(signalChan, func(c workflow.ReceiveChannel, more bool) {
		// Operator cancelled: consume the (empty) signal payload and take the
		// restore path.
		var payload struct{}
		c.Receive(ctx, &payload)
		cancelled = true
		logger.Info("deletion cancelled by signal during grace period", "TenantID", in.TenantID)
	})
	selector.Select(ctx)

	if cancelled {
		if err = workflow.ExecuteActivity(actCtx, activities.RestoreTenantAfterCancelActivityName, in.TenantID).Get(actCtx, nil); err != nil {
			return err
		}
		logger.Info("DeleteTenantWorkflow cancelled during grace period", "TenantID", in.TenantID, "WorkflowID", workflowID)
		return nil
	}

	if err = workflow.ExecuteActivity(actCtx, activities.DeprovisionTenantActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	if err = workflow.ExecuteActivity(actCtx, activities.MarkTenantDeletedActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	logger.Info("DeleteTenantWorkflow completed successfully", "TenantID", in.TenantID, "WorkflowID", workflowID)
	return nil
}
