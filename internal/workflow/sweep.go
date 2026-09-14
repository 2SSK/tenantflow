package workflow

import (
	"errors"
	"fmt"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/2SSK/tenantflow/internal/activities"
)

// ReconcileSweepWorkflowID is the fixed workflow ID of the scheduled sweep
// loop. A fixed ID plus ALLOW_DUPLICATE gives an important restart property:
// a worker reboot that calls the boot-time start finds the existing sweep
// still running and skips (in-flight guard), while a sweep that has truly
// gone away is allowed to restart.
const ReconcileSweepWorkflowID = "reconcile-sweep"

// defaultSweepsBeforeContinueAsNew bounds each sweep run's history size. A
// workflow that runs forever would accumulate history forever (every event
// replayed on every wakeup), so every Nth sweep the run ends itself and
// starts a fresh run with the same input — history is reset, intent is kept.
const defaultSweepsBeforeContinueAsNew = 100

// ReconcileSweepInput configures the sweep loop.
//
// Interval is the gap between sweeps. MaxSweeps > 0 makes the loop stop after
// that many sweeps (a one-shot "sweep now" mode and how tests bound the
// otherwise-infinite loop); 0 means run forever. ContinueAfter overrides the
// ContinueAsNew cadence (tests use a small value); 0 means the default.
type ReconcileSweepInput struct {
	Interval      time.Duration
	MaxSweeps     int
	ContinueAfter int
}

// ReconcileSweepResult summarizes what the (finite) run did. The forever loop
// never returns it — it only completes via cancellation.
type ReconcileSweepResult struct {
	SweepsCompleted  int
	LastTenantsFound int
}

// ReconcileSweepWorkflow is the scheduled reconciliation driver (Phase 14).
//
// Each tick it enumerates every ACTIVE tenant and starts one
// ReconcileTenantWorkflow per tenant, then sleeps until the next tick. It is
// deliberately a dumb driver: all judgment lives in the per-tenant workflow
// that already exists (probe → repair-or-escalate → re-probe).
//
// Concurrency with the manual endpoint is handled by design: the sweep starts
// children under the SAME workflow ID the manual POST /reconcile uses
// (reconcile-<tenantID>). ALLOW_DUPLICATE keeps closed reconcile runs
// re-runnable (a converged tenant must not become exempt), while Temporal
// still refuses to start a NEW run while a workflow with that ID is IN FLIGHT
// — the collision surfaces as WorkflowExecutionAlreadyStarted on the child
// future, which this loop treats as "someone else is already handling it,
// skip". The same guard protects the sweep from stepping on itself across two
// workers.
//
// Flow per tick:
//
//	list active tenants
//	        ↓
//	start reconcile-<id> per tenant (dedup: same ID twice in one list → once;
//	                                in-flight elsewhere → AlreadyStarted → skip)
//	        ↓
//	sweeps++; every ContinueAfter sweeps → ContinueAsNew (history reset)
//	        ↓
//	sleep(Interval) ──► next tick
func ReconcileSweepWorkflow(ctx workflow.Context, in ReconcileSweepInput) (*ReconcileSweepResult, error) {
	logger := workflow.GetLogger(ctx)

	if in.Interval <= 0 {
		// A non-retryable failure is the honest failure mode: a sweep that
		// runs in a hot loop would be far worse than one that fails loudly,
		// and nothing about the input will change on retry.
		return nil, temporal.NewNonRetryableApplicationError(
			"reconcile sweep: Interval must be > 0", "InvalidSweepInterval", nil)
	}

	continueAfter := in.ContinueAfter
	if continueAfter <= 0 {
		continueAfter = defaultSweepsBeforeContinueAsNew
	}

	sweeps := 0
	lastFound := 0

	for {
		var ids []string
		if err := workflow.ExecuteActivity(
			workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
				StartToCloseTimeout: time.Minute,
			}),
			activities.ListActiveTenantIDsActivityName,
		).Get(ctx, &ids); err != nil {
			// The activity has its own retry policy; if it still fails, the
			// workflow retry restarts this sweep run — the alternative (keep
			// sweeping blind) would reconcile tenants against an unknown world.
			return nil, fmt.Errorf("sweep enumeration failed: %w", err)
		}
		lastFound = len(ids)

		// Dedup the list (defensive: an active tenant must appear once).
		type sweptChild struct {
			id  string
			fut workflow.Future
		}
		started := make(map[string]struct{}, len(ids))
		children := make([]sweptChild, 0, len(ids))
		for _, id := range ids {
			if _, ok := started[id]; ok {
				logger.Info("sweep: duplicate tenant in list, skipping second start", "tenantID", id)
				continue
			}
			started[id] = struct{}{}

			child := workflow.ExecuteChildWorkflow(
				workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
					WorkflowID: "reconcile-" + id,
					// ALLOW_DUPLICATE (NOT REJECT_DUPLICATE): same reasoning as
					// the manual endpoint comment — reconciliation must stay
					// re-runnable after a completed run. Temporal still refuses
					// to start a new run while one with this ID is IN FLIGHT,
					// so the same-ID collision with a manual reconcile (or a
					// second worker's sweep) surfaces as
					// WorkflowExecutionAlreadyStarted on the child future.
					WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
				}),
				ReconcileTenantWorkflow, ReconcileInput{TenantID: id})
			children = append(children, sweptChild{id: id, fut: child})
		}
		sweeps++

		if in.MaxSweeps > 0 && sweeps >= in.MaxSweeps {
			// One-shot ("sweep now") mode: wait for this tick's batch so the
			// caller of the workflow knows the sweep actually finished before
			// it reports/completes.
			for _, c := range children {
				observeSweptReconcile(ctx, logger, c.id, c.fut, true)
			}
			logger.Info("sweep: finished", "sweeps", sweeps, "tenantsFound", lastFound)
			return &ReconcileSweepResult{SweepsCompleted: sweeps, LastTenantsFound: lastFound}, nil
		}

		if sweeps%continueAfter == 0 {
			logger.Info("sweep: continue as new", "sweeps", sweeps)
			return nil, workflow.NewContinueAsNewError(ctx, ReconcileSweepWorkflow, ReconcileSweepInput{
				Interval:      in.Interval,
				MaxSweeps:     in.MaxSweeps,
				ContinueAfter: in.ContinueAfter,
			})
		}

		// Forever loop: fire-and-forget from the driver's perspective — the
		// tick must not wait for the slowest tenant. Observe each child in a
		// side goroutine so failures are logged, not dropped.
		for _, c := range children {
			workflow.Go(ctx, func(ctx workflow.Context) {
				observeSweptReconcile(ctx, logger, c.id, c.fut, false)
			})
		}

		if err := workflow.Sleep(ctx, in.Interval); err != nil {
			return nil, err
		}
	}
}

// observeSweptReconcile waits for one sweep-started reconcile child and turns
// its outcome into a log line. An in-flight collision is a normal SKIP (the
// same tenant is already being reconciled by a manual call or an older sweep
// tick, so this tick has nothing to add); any other failure is logged for the
// operator while the DLQ/failed-runs view keeps the machine-readable record.
func observeSweptReconcile(ctx workflow.Context, logger log.Logger, tenantID string, fut workflow.Future, _ bool) {
	var res ReconcileResult
	err := fut.Get(ctx, &res)
	if err == nil {
		logger.Info("sweep: scheduled reconcile converged", "tenantID", tenantID, "converged", res.Converged)
		return
	}
	var already *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &already) {
		logger.Info("sweep: reconcile already in flight, skipping", "tenantID", tenantID)
		return
	}
	logger.Warn("sweep: scheduled reconcile failed", "tenantID", tenantID, "error", err)
}
