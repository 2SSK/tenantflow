package workflow

import (
	"errors"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/2SSK/tenantflow/internal/activities"
	"github.com/2SSK/tenantflow/internal/model"
)

// DeleteTenantWorkflowName is the workflow type the recorder stores for
// failed delete runs, and the key the DLQ replay uses to resume a stuck
// deletion instead of re-provisioning.
const DeleteTenantWorkflowName = "DeleteTenantWorkflow"

// CancelDeleteSignalName is the signal an operator sends to stop an in-flight
// deletion during the grace period. The handler (POST /cancel-delete) signals
// the running workflow by this name.
const CancelDeleteSignalName = "cancel-delete"

type DeleteInput struct {
	TenantID string
	// GracePeriod is the durable wait before teardown starts. Ignored when
	// Resume is true — a resumed deletion already lost its grace window.
	GracePeriod time.Duration
	// Resume continues teardown for a tenant stuck in "deleting" (a delete
	// workflow that failed permanently after the CAS committed). Resume skips
	// the state transition and the grace period: it picks up where the failed
	// run stopped. Started by the DLQ replay endpoint, never by DELETE.
	Resume bool
	// IsolationMode decides whether the pre-delete backup runs. Shared-schema
	// tenants have no tenant_<id> database, so there is nothing to snapshot;
	// attempting one makes the delete saga fail permanently (the load test
	// found 100/100 shared deletes stuck in the DLQ this way). An empty value
	// is treated as dedicated — the default mode and the shape older inputs
	// carry.
	IsolationMode model.IsolationMode
}

// teardownBackupVersion is the workflow-version marker for the v1 teardown
// enhancement: capturing a verified pre-delete backup before destroying the
// tenant's data (a retention-policy requirement).
//
// workflow.GetVersion makes this code change compatible with in-flight
// executions. A deletion started before this code shipped has no marker in
// its history, so when the new worker replays it GetVersion returns
// workflow.DefaultVersion and the teardown keeps the OLD behavior — the SDK
// deliberately tolerates the unmarked branch. Only executions whose first
// decision runs on v1 code record the marker and take the backup branch.
// Without the gate, the added ExecuteActivity would be a brand-new command
// on an old history and the worker would fail the run with
// NonDeterministicError.
const teardownBackupVersion workflow.Version = 1

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
//	v1 teardown (version-gated): a verified pre-delete backup is captured via
//	BackupTenantData before DeprovisionTenant runs, so a deleted tenant still
//	leaves a restorable artifact behind.
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
//
// Resume (DLQ replay): a stuck "deleting" tenant has no operator lever today —
// cancel-delete needs a live workflow and retry only replays provisioning. A
// resumed run skips the CAS transition and the grace timer and runs straight
// into teardown, ending with the same MarkTenantDeleted CAS as a normal run.
// Teardown is idempotent (drop database / role IF EXISTS; identity delete
// treats a missing user as success), so replaying it is safe.
func DeleteTenantWorkflow(ctx workflow.Context, in DeleteInput) (err error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("Starting DeleteTenantWorkflow", "TenantID", in.TenantID, "GracePeriod", in.GracePeriod, "Resume", in.Resume)

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

	// A resume is a continuation, not a fresh delete: the tenant is already
	// "deleting" (the CAS that got there was committed by the run this replay
	// replaces) and the operator already had their grace window. Both steps
	// below are a new start only.
	if !in.Resume {
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
	}

	// ── v1: capture a verified pre-delete backup before any destruction. ──
	// Gated by GetVersion so deletions started before this shipped keep their
	// original semantics: their history lacks the marker, so on replay this
	// returns DefaultVersion and the branch is skipped.
	if workflow.GetVersion(actCtx, "tf-delete-preteardown-backup", workflow.DefaultVersion, teardownBackupVersion) >= teardownBackupVersion {
		// Shared-schema tenants share the platform database and have no
		// tenant_<id> database to snapshot: BackupTenantData would pg_dump a
		// nonexistent database, exhaust its retries, and strand the deletion
		// in the DLQ forever (observed live in the 12.2 load run). Skipping
		// the backup for them is the only correct behavior — there is no
		// per-tenant artifact to retain, and teardown continues normally.
		if in.IsolationMode == model.IsolationModeShared {
			logger.Info("skipping pre-delete backup: shared tenant has no dedicated database", "TenantID", in.TenantID)
		} else {
			var preDeleteBackup *model.Backup
			if err = workflow.ExecuteActivity(actCtx, activities.BackupTenantDataActivityName, in.TenantID).Get(actCtx, &preDeleteBackup); err != nil {
				return err
			}
			logger.Info("teardown v1: pre-delete backup captured", "TenantID", in.TenantID, "BackupID", preDeleteBackup.ID, "Filename", preDeleteBackup.Filename)
		}
	}

	if err = workflow.ExecuteActivity(actCtx, activities.DeprovisionTenantActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	// Identity teardown completes the saga: remove the Keycloak user the
	// provision run created. This run can't read provision's userID variable,
	// so the activity derives the username from tenantID and no-ops when it is
	// already gone — a DLQ replay of a half-torn tenant converges.
	if err = workflow.ExecuteActivity(actCtx, activities.DeleteTenantIdentityByTenantActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	if err = workflow.ExecuteActivity(actCtx, activities.MarkTenantDeletedActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	logger.Info("DeleteTenantWorkflow completed successfully", "TenantID", in.TenantID, "WorkflowID", workflowID)
	return nil
}
