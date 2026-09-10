package activities

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/2SSK/tenantflow/internal/cloud"
	"github.com/2SSK/tenantflow/internal/metrics"
	"github.com/2SSK/tenantflow/internal/model"
	"github.com/2SSK/tenantflow/internal/repository"
)

const (
	ResolveTenantSpecActivityName      = "ResolveTenantSpec"
	ProbeTenantActualStateActivityName = "ProbeTenantActualState"
	EnsureTenantDatabaseActivityName   = "EnsureTenantDatabase"
	RecordReconcileDriftActivityName   = "RecordReconcileDrift"
	MarkReconcileConvergedActivityName = "MarkReconcileConverged"
	MarkReconcileSkippedActivityName   = "MarkReconcileSkipped"
	MarkReconcileFailedActivityName    = "MarkReconcileFailed"
)

// ReconcileActivities implement the reconciliation loop's legs: resolve the
// DESIRED state, probe the ACTUAL state, repair drift with provider
// primitives, and record what happened in the audit trail.
type ReconcileActivities struct {
	repo       repository.TenantRepository
	auditRepo  repository.AuditRepository
	backupRepo repository.BackupRepository
	provider   cloud.CloudProvider
	// reg feeds the low-cardinality reconcile metrics. Test wiring may pass
	// nil; every use is guarded.
	reg *metrics.Registry
}

func NewReconcileActivities(repo repository.TenantRepository, auditRepo repository.AuditRepository, backupRepo repository.BackupRepository, provider cloud.CloudProvider, reg *metrics.Registry) *ReconcileActivities {
	return &ReconcileActivities{repo: repo, auditRepo: auditRepo, backupRepo: backupRepo, provider: provider, reg: reg}
}

// ResolveTenantSpec materializes the DESIRED state: the tenants row plus the
// platform policy derived from it. A missing row is non-retryable — the DLQ,
// not infinite retries, is the right home for it.
func (a *ReconcileActivities) ResolveTenantSpec(ctx context.Context, tenantID string) (model.TenantSpec, error) {
	activity.GetLogger(ctx).Info("resolving tenant spec", "tenantID", tenantID)

	tenant, err := a.repo.GetTenant(ctx, tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return model.TenantSpec{}, temporal.NewNonRetryableApplicationError(
				"tenant "+tenantID+" not found", "TenantNotFound", err)
		}
		return model.TenantSpec{}, fmt.Errorf("get tenant %s: %w", tenantID, err)
	}

	dedicated := tenant.IsolationMode == model.IsolationModeDedicated
	return model.TenantSpec{
		TenantID:        tenant.TenantID,
		Status:          tenant.Status,
		IsolationMode:   tenant.IsolationMode,
		RequireDatabase: dedicated,
		RequireBackup:   dedicated,
	}, nil
}

// ProbeTenantActualState reads the real world: the provider reports on the
// tenant database and role, the backup repository reports completed backups.
// All-or-nothing: if the probe cannot see infrastructure the workflow must
// fail (and retry / DLQ), not reconcile blind.
func (a *ReconcileActivities) ProbeTenantActualState(ctx context.Context, spec model.TenantSpec) (model.ReconcileActualState, error) {
	activity.GetLogger(ctx).Info("probing tenant actual state", "tenantID", spec.TenantID, "isolationMode", spec.IsolationMode)

	state := model.ReconcileActualState{TenantID: spec.TenantID}

	if spec.RequireDatabase {
		dbState, err := a.provider.InspectDatabase(ctx, cloud.TenantDatabaseName(spec.TenantID))
		if err != nil {
			return model.ReconcileActualState{}, fmt.Errorf("probe database for tenant %s: %w", spec.TenantID, err)
		}
		state.Database = model.DatabaseActualState{
			Exists:        dbState.Exists,
			OwnerRole:     dbState.OwnerRole,
			PublicConnect: dbState.PublicConnect,
		}
		roleExists, err := a.provider.RoleExists(ctx, cloud.TenantDatabaseName(spec.TenantID))
		if err != nil {
			return model.ReconcileActualState{}, fmt.Errorf("probe role for tenant %s: %w", spec.TenantID, err)
		}
		state.RoleExists = roleExists
	}

	if spec.RequireBackup {
		backups, err := a.backupRepo.ListBackups(ctx, spec.TenantID)
		if err != nil {
			return model.ReconcileActualState{}, fmt.Errorf("probe backups for tenant %s: %w", spec.TenantID, err)
		}
		for _, b := range backups {
			if b.Status == model.BackupStatusCompleted {
				state.CompletedBackups++
			}
		}
	}

	return state, nil
}

// DetectDrifts compares DESIRED vs ACTUAL and returns the drift kinds. It is
// a pure function (no I/O) so the workflow is deterministic and the logic is
// unit-testable without Temporal. The expected owner role comes from the same
// cloud.TenantDatabaseName rule the provider uses to create databases, so the
// comparison can never disagree with what the provider actually builds.
func DetectDrifts(spec model.TenantSpec, actual model.ReconcileActualState) []string {
	if !spec.RequireDatabase {
		// Shared tenants share the platform database; the plane promises no
		// per-tenant database, role, or per-tenant backup to enforce here.
		return nil
	}

	expectedRole := cloud.TenantDatabaseName(spec.TenantID)

	var drifts []string
	if !actual.Database.Exists {
		drifts = append(drifts, model.DriftMissingDatabase)
	}
	if !actual.RoleExists {
		drifts = append(drifts, model.DriftMissingRole)
	}
	if actual.Database.Exists && actual.Database.OwnerRole != expectedRole {
		drifts = append(drifts, model.DriftWrongOwner)
	}
	if actual.Database.Exists && actual.Database.PublicConnect {
		drifts = append(drifts, model.DriftPublicConnect)
	}
	if spec.RequireBackup && actual.CompletedBackups == 0 {
		drifts = append(drifts, model.DriftMissingBackup)
	}
	return drifts
}

// EnsureTenantDatabase is the database repair primitive: make the tenant's
// database exist with the correct isolation shape. A missing database is
// created with ownership applied (role + owner + REVOKE CONNECT) in one
// call; an existing-but-drifted one is repaired in place. Both paths are
// idempotent under Temporal retries.
func (a *ReconcileActivities) EnsureTenantDatabase(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("ensuring tenant database", "tenantID", tenantID)

	dbName := cloud.TenantDatabaseName(tenantID)
	state, err := a.provider.InspectDatabase(ctx, dbName)
	if err != nil {
		return fmt.Errorf("inspect before ensure for tenant %s: %w", tenantID, err)
	}
	if !state.Exists {
		if err := a.provider.CreateDatabase(ctx, tenantID); err != nil {
			return fmt.Errorf("create missing database for tenant %s: %w", tenantID, err)
		}
		return nil
	}
	if err := a.provider.EnsureDatabaseOwnership(ctx, dbName); err != nil {
		return fmt.Errorf("repair ownership for tenant %s: %w", tenantID, err)
	}
	return nil
}

// RecordReconcileDrift audits the detected drift kinds and feeds the
// platform-wide drift metric. Per-tenant drift detail lives in the audit
// trail; the metric aggregates by kind only (no tenant label).
func (a *ReconcileActivities) RecordReconcileDrift(ctx context.Context, tenantID string, kinds []string) error {
	activity.GetLogger(ctx).Info("reconciliation drift detected", "tenantID", tenantID, "drifts", kinds)

	for _, kind := range kinds {
		if a.reg != nil {
			a.reg.DriftEvents.WithLabelValues(kind).Inc()
		}
	}
	return a.writeAudit(ctx, tenantID, model.AuditEventTenantDriftDetected, map[string]any{"drifts": kinds})
}

// MarkReconcileConverged audits the outcome. result="converged" when nothing
// had drifted, "repaired" when drift was fixed.
func (a *ReconcileActivities) MarkReconcileConverged(ctx context.Context, tenantID string, repaired []string) error {
	activity.GetLogger(ctx).Info("reconciliation converged", "tenantID", tenantID, "repaired", repaired)

	result := "converged"
	if len(repaired) > 0 {
		result = "repaired"
	}
	if a.reg != nil {
		a.reg.ReconcileRuns.WithLabelValues(result).Inc()
	}
	return a.writeAudit(ctx, tenantID, model.AuditEventTenantReconcileConverged, map[string]any{"converged": true, "repaired": repaired})
}

// MarkReconcileSkipped records tenants the plane refuses to reconcile (any
// status other than active converges by construction — failed/deleted tenants
// are already compensated, deleting tenants are being torn down).
func (a *ReconcileActivities) MarkReconcileSkipped(ctx context.Context, tenantID, reason string) error {
	activity.GetLogger(ctx).Info("reconciliation skipped", "tenantID", tenantID, "reason", reason)

	if a.reg != nil {
		a.reg.ReconcileRuns.WithLabelValues("skipped").Inc()
	}
	return a.writeAudit(ctx, tenantID, model.AuditEventTenantReconcileSkipped, map[string]any{"reason": reason})
}

// MarkReconcileFailed records a reconciliation that could not converge (drift
// remained after repair, or the run errored past retries). The failed
// workflow instance lands in the DLQ so an operator can retry it.
func (a *ReconcileActivities) MarkReconcileFailed(ctx context.Context, tenantID, reason string) error {
	activity.GetLogger(ctx).Info("reconciliation failed", "tenantID", tenantID, "reason", reason)

	if a.reg != nil {
		a.reg.ReconcileRuns.WithLabelValues("failed").Inc()
	}
	return a.writeAudit(ctx, tenantID, model.AuditEventTenantReconcileFailed, map[string]any{"reason": reason})
}

func (a *ReconcileActivities) writeAudit(ctx context.Context, tenantID string, eventType model.AuditEventType, payload map[string]any) error {
	workflowID := activity.GetInfo(ctx).WorkflowExecution.ID
	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:   tenantID,
		WorkflowID: &workflowID,
		EventType:  eventType,
		Actor:      "workflow",
		Payload:    payload,
	})
}
