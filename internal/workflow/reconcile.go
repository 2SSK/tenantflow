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
// Scope: this is DATABASE-INFRASTRUCTURE reconciliation for dedicated tenants
// (database exists + isolation shape + owner role + backup policy). It does
// not reconcile tenant identity, application data, or shared-schema rows —
// anything not covered by a provider/backup primitive drifts undetected.
//
// Flow:
//
//	resolve spec ──► probe ──► detect drift ──► (none) ──► converged, done
//	                                │
//	                                └──► record drift ──► repair ──► re-probe
//	                                                               │
//	                                              still drifted ──┴──► fail (DLQ)
//
// DATA-SAFETY RULE (missing database): an active dedicated tenant's database
// once existed, so a missing database is potential data loss. The only safe
// repairs are (1) restore from the latest verified backup, or (2) escalate to
// the DLQ when no verified backup exists. Synthesizing an empty replacement
// database and calling the tenant converged would DESTROY the tenant's data
// (and the operator's ability to notice). The restore/escalate decision is
// made in an activity and signalled back via the ErrNoVerifiedBackup sentinel
// so the workflow stays deterministic.
//
// A failed or unrecoverable run lands in the failed-runs/DLQ view like every
// other workflow, so an operator can retry it after the underlying cause is
// fixed.
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

	// Repair — with the missing-database data-safety rule FIRST. A missing
	// database is potential data loss, so restore from the latest verified
	// backup before touching anything else; escalate to the DLQ (non-retryable
	// — retrying will not manufacture a backup) when there is no verified
	// backup. We NEVER fall through to EnsureTenantDatabase's create-empty
	// path for a missing database: that would look like convergence while
	// silently destroying the tenant's data.
	if slices.Contains(drifts, model.DriftMissingDatabase) {
		err := workflow.ExecuteActivity(actCtx, activities.RestoreTenantFromBackupActivityName,
			in.TenantID).Get(actCtx, nil)
		if activities.IsNoVerifiedBackup(err) {
			reason := fmt.Sprintf("tenant %s database is missing and no verified backup exists; "+
				"refusing to synthesize an empty replacement (data unrecoverable) — restore or delete manually",
				in.TenantID)
			_ = workflow.ExecuteActivity(actCtx, activities.MarkReconcileUnrecoverableActivityName,
				in.TenantID, reason).Get(actCtx, nil)
			return &ReconcileResult{TenantID: in.TenantID, Converged: false, Drifts: drifts},
				temporal.NewNonRetryableApplicationError(reason, "ReconcileUnrecoverable", err)
		}
		if err != nil {
			return nil, err
		}
	}
	// The remaining database/role/ownership/connect drifts are shape-only and
	// safe to auto-repair. EnsureTenantDatabase is idempotent and re-applies
	// ownership after a data restore, so running it after a restore is
	// harmless (and repairs any drift the dump's statements introduced).
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
