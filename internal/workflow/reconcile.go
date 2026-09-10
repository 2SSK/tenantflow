package workflow

import (
	"fmt"
	"slices"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/2SSK/tenantflow/internal/activities"
	"github.com/2SSK/tenantflow/internal/model"
)

// ReconcileInput names the tenant whose infrastructure must be converged back
// to its desired state.
type ReconcileInput struct {
	TenantID string
}

// ReconcileResult is what a reconciliation run concluded. Drifts lists what
// was wrong before repair; Repaired lists what the run fixed (empty on the
// already-converged fast path).
type ReconcileResult struct {
	TenantID  string
	Converged bool
	Drifts    []string `json:"drifts,omitempty"`
	Repaired  []string `json:"repaired,omitempty"`
}

// ReconcileTenantWorkflow is the reconciliation loop: resolve the tenant's
// DESIRED state, probe ACTUAL infrastructure, compare, repair what drifted by
// reusing the same provider primitives the other sagas use, then RE-PROBE to
// prove convergence instead of trusting the repair.
//
// Flow:
//
//	resolve spec ──► probe ──► detect drift ──► (none) ──► converged, done
//	                                │
//	                                └──► record drift ──► repair ──► re-probe
//	                                                               │
//	                                              still drifted ──┴──► fail (DLQ)
//
// A failed run lands in the failed-runs/DLQ view like every other workflow,
// so an operator can retry it after the underlying cause is fixed.
func ReconcileTenantWorkflow(ctx workflow.Context, in ReconcileInput) (*ReconcileResult, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("reconcile workflow started", "tenantID", in.TenantID)

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 3 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})

	var spec model.TenantSpec
	if err := workflow.ExecuteActivity(actCtx, activities.ResolveTenantSpecActivityName, in.TenantID).Get(actCtx, &spec); err != nil {
		return nil, err
	}

	// Non-active tenants converge by construction: provisioned/upgrading/
	// migrating tenants are mid-saga (their workflows own the outcome), and
	// failed/deleted tenants were already compensated so nothing is promised.
	// Record the skip so the operator sees the decision.
	if spec.Status != model.TenantStatusActive {
		if err := workflow.ExecuteActivity(actCtx, activities.MarkReconcileSkippedActivityName,
			in.TenantID, "tenant status "+string(spec.Status)).Get(actCtx, nil); err != nil {
			return nil, err
		}
		return &ReconcileResult{TenantID: in.TenantID, Converged: true}, nil
	}

	// ACTUAL state.
	var actual model.ReconcileActualState
	if err := workflow.ExecuteActivity(actCtx, activities.ProbeTenantActualStateActivityName, spec).Get(actCtx, &actual); err != nil {
		return nil, err
	}

	// DESIRED vs ACTUAL.
	drifts := activities.DetectDrifts(spec, actual)
	if len(drifts) == 0 {
		if err := workflow.ExecuteActivity(actCtx, activities.MarkReconcileConvergedActivityName,
			in.TenantID, []string{}).Get(actCtx, nil); err != nil {
			return nil, err
		}
		return &ReconcileResult{TenantID: in.TenantID, Converged: true}, nil
	}

	if err := workflow.ExecuteActivity(actCtx, activities.RecordReconcileDriftActivityName,
		in.TenantID, drifts).Get(actCtx, nil); err != nil {
		return nil, err
	}

	// Repair. One database action covers every database/role/ownership drift
	// kind; a missing backup is a second, independent repair.
	if needsDatabaseRepair(drifts) {
		if err := workflow.ExecuteActivity(actCtx, activities.EnsureTenantDatabaseActivityName,
			in.TenantID).Get(actCtx, nil); err != nil {
			return nil, err
		}
	}
	if slices.Contains(drifts, model.DriftMissingBackup) {
		var backup *model.Backup
		if err := workflow.ExecuteActivity(actCtx, activities.BackupTenantDataActivityName,
			in.TenantID).Get(actCtx, &backup); err != nil {
			return nil, err
		}
	}

	// Prove the repair: re-probe and re-compare. A tenant still drifted after
	// repair is UNREPAIRABLE right now — surface that as a failed run (DLQ)
	// instead of claiming convergence.
	var after model.ReconcileActualState
	if err := workflow.ExecuteActivity(actCtx, activities.ProbeTenantActualStateActivityName, spec).Get(actCtx, &after); err != nil {
		return nil, err
	}
	if remaining := activities.DetectDrifts(spec, after); len(remaining) > 0 {
		reason := fmt.Sprintf("drift remains after reconciliation: %v", remaining)
		_ = workflow.ExecuteActivity(actCtx, activities.MarkReconcileFailedActivityName,
			in.TenantID, reason).Get(actCtx, nil)
		return &ReconcileResult{TenantID: in.TenantID, Converged: false, Drifts: drifts, Repaired: drifts},
			temporal.NewApplicationError(reason, "ReconcileUnconverged", nil)
	}

	if err := workflow.ExecuteActivity(actCtx, activities.MarkReconcileConvergedActivityName,
		in.TenantID, drifts).Get(actCtx, nil); err != nil {
		return nil, err
	}
	return &ReconcileResult{TenantID: in.TenantID, Converged: true, Drifts: drifts, Repaired: drifts}, nil
}

// needsDatabaseRepair reports whether any drift kind is fixed by the single
// EnsureTenantDatabase action (database, role, ownership, PUBLIC CONNECT).
func needsDatabaseRepair(drifts []string) bool {
	for _, d := range drifts {
		switch d {
		case model.DriftMissingDatabase, model.DriftMissingRole, model.DriftWrongOwner, model.DriftPublicConnect:
			return true
		}
	}
	return false
}
