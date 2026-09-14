package activities

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/2SSK/tenantflow/internal/cloud"
	"github.com/2SSK/tenantflow/internal/metrics"
	"github.com/2SSK/tenantflow/internal/model"
	"github.com/2SSK/tenantflow/internal/repository"
)

const (
	ResolveTenantSpecActivityName          = "ResolveTenantSpec"
	ProbeTenantActualStateActivityName     = "ProbeTenantActualState"
	EnsureTenantDatabaseActivityName       = "EnsureTenantDatabase"
	RecordReconcileDriftActivityName       = "RecordReconcileDrift"
	MarkReconcileConvergedActivityName     = "MarkReconcileConverged"
	MarkReconcileSkippedActivityName       = "MarkReconcileSkipped"
	MarkReconcileFailedActivityName        = "MarkReconcileFailed"
	RestoreTenantFromBackupActivityName    = "RestoreTenantFromBackup"
	MarkReconcileUnrecoverableActivityName = "MarkReconcileUnrecoverable"
	// ListActiveTenantIDsActivityName feeds the scheduled reconcile sweep
	// (Phase 14): each tick the sweep asks which tenants need attention.
	ListActiveTenantIDsActivityName = "ListActiveTenantIDs"
)

// ErrNoVerifiedBackup is the sentinel RestoreTenantFromBackup returns when the
// tenant has no completed, verifiable backup to restore from. The workflow
// treats it specially: escalate to the DLQ instead of retrying (a retry won't
// manufacture a backup that never existed) and NEVER fall back to creating an
// empty replacement database.
var ErrNoVerifiedBackup = errors.New("no verified backup to restore from")

// NoVerifiedBackupErrorType is the Temporal ApplicationError type carries the
// sentinel across the activity boundary (plain errors lose identity when
// serialized, so the workflow checks the typed error instead of errors.Is on
// a raw sentinel).
const NoVerifiedBackupErrorType = "NoVerifiedBackup"

// NewNoVerifiedBackupError wraps the sentinel in a NON-RETRYABLE typed
// ApplicationError: retrying cannot manufacture a backup, so the escalation
// must skip both the activity retry policy AND the workflow retry path and
// reach the DLQ immediately.
func NewNoVerifiedBackupError() error {
	return temporal.NewNonRetryableApplicationError(ErrNoVerifiedBackup.Error(), NoVerifiedBackupErrorType, ErrNoVerifiedBackup)
}

// IsNoVerifiedBackup reports whether err is the no-verified-backup escalation,
// whether it arrives as the raw sentinel (in-process) or as the typed
// ApplicationError (across a worker boundary or a mocked activity).
func IsNoVerifiedBackup(err error) bool {
	if errors.Is(err, ErrNoVerifiedBackup) {
		return true
	}
	var appErr *temporal.ApplicationError
	return errors.As(err, &appErr) && appErr.Type() == NoVerifiedBackupErrorType
}

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
	logFor(ctx).Info("resolving tenant spec", "tenantID", tenantID)

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

// ListActiveTenantIDs returns the tenants the scheduled sweep should look at:
// every ledger row in the active state. A deterministic (sorted) order keeps
// the sweep's behavior stable across replays and workers.
func (a *ReconcileActivities) ListActiveTenantIDs(ctx context.Context) ([]string, error) {
	tenants, err := a.repo.ListTenants(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	ids := make([]string, 0, len(tenants))
	for _, t := range tenants {
		if t.Status == model.TenantStatusActive {
			ids = append(ids, t.TenantID)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// ProbeTenantActualState reads the real world: the provider reports on the
// tenant database and role, the backup repository reports completed backups.
// All-or-nothing: if the probe cannot see infrastructure the workflow must
// fail (and retry / DLQ), not reconcile blind.
func (a *ReconcileActivities) ProbeTenantActualState(ctx context.Context, spec model.TenantSpec) (model.ReconcileActualState, error) {
	logFor(ctx).Info("probing tenant actual state", "tenantID", spec.TenantID, "isolationMode", spec.IsolationMode)

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
	logFor(ctx).Info("ensuring tenant database", "tenantID", tenantID)

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
	logFor(ctx).Info("reconciliation drift detected", "tenantID", tenantID, "drifts", kinds)

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
	logFor(ctx).Info("reconciliation converged", "tenantID", tenantID, "repaired", repaired)

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
	logFor(ctx).Info("reconciliation skipped", "tenantID", tenantID, "reason", reason)

	if a.reg != nil {
		a.reg.ReconcileRuns.WithLabelValues("skipped").Inc()
	}
	return a.writeAudit(ctx, tenantID, model.AuditEventTenantReconcileSkipped, map[string]any{"reason": reason})
}

// MarkReconcileFailed records a reconciliation that could not converge (drift
// remained after repair, or the run errored past retries). The failed
// workflow instance lands in the DLQ so an operator can retry it.
func (a *ReconcileActivities) MarkReconcileFailed(ctx context.Context, tenantID, reason string) error {
	logFor(ctx).Info("reconciliation failed", "tenantID", tenantID, "reason", reason)

	if a.reg != nil {
		a.reg.ReconcileRuns.WithLabelValues("failed").Inc()
	}
	return a.writeAudit(ctx, tenantID, model.AuditEventTenantReconcileFailed, map[string]any{"reason": reason})
}

// RestoreTenantFromBackup is the data-safety repair for a MISSING database:
// recreate the database with the tenant's latest verified backup and prove it
// is valid. An active dedicated tenant's database once existed, so a missing
// database is potential data loss — the only safe repairs are "restore the
// data" or "escalate to an operator"; synthesizing an empty database is
// neither and must never happen.
//
// Returns ErrNoVerifiedBackup (sentinel) when no completed backup exists so
// the workflow can escalate instead of pretending to converge.
//
// The provider's restore primitive loads a dump into an EXISTING database, so
// this activity creates the database first (with correct ownership), then
// restores, then re-applies ownership and validates. All three primitives are
// idempotent under Temporal retries.
func (a *ReconcileActivities) RestoreTenantFromBackup(ctx context.Context, tenantID string) error {
	logFor(ctx).Info("restoring tenant database from latest verified backup", "tenantID", tenantID)

	backups, err := a.backupRepo.ListBackups(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("list backups for tenant %s: %w", tenantID, err)
	}
	// ListBackups orders by created_at DESC, so the first completed row is the
	// newest verifiable point-in-time snapshot.
	var latest *model.Backup
	for i := range backups {
		if backups[i].Status == model.BackupStatusCompleted {
			latest = &backups[i]
			break
		}
	}
	if latest == nil {
		return NewNoVerifiedBackupError()
	}

	dbName := cloud.TenantDatabaseName(tenantID)
	if err := a.provider.CreateDatabase(ctx, tenantID); err != nil {
		return fmt.Errorf("recreate database for tenant %s: %w", tenantID, err)
	}
	if err := a.provider.RestoreDatabaseFromBackup(ctx, dbName, latest.Filename); err != nil {
		return fmt.Errorf("restore backup %q into %s: %w", latest.Filename, dbName, err)
	}
	// A dump can carry its own ownership/connect statements; re-apply the
	// tenant isolation shape so the restored database matches the desired
	// state by construction.
	if err := a.provider.EnsureDatabaseOwnership(ctx, dbName); err != nil {
		return fmt.Errorf("re-apply ownership after restore for tenant %s: %w", tenantID, err)
	}
	if err := a.provider.ValidateDatabase(ctx, dbName); err != nil {
		return fmt.Errorf("validate restored database %s: %w", dbName, err)
	}

	return a.writeAudit(ctx, tenantID, model.AuditEventTenantReconcileRestored,
		map[string]any{"backup_id": latest.ID, "filename": latest.Filename})
}

// MarkReconcileUnrecoverable records a run the plane refuses to auto-repair:
// the tenant's database is missing and no verified backup exists to restore
// it from. The workflow then returns a NON-RETRYABLE error so the instance
// goes straight to the DLQ (retrying cannot help, and pretending to converge
// would destroy tenant data perception of safety).
func (a *ReconcileActivities) MarkReconcileUnrecoverable(ctx context.Context, tenantID, reason string) error {
	logFor(ctx).Error("reconciliation unrecoverable, escalating to operator", "tenantID", tenantID, "reason", reason)

	if a.reg != nil {
		a.reg.ReconcileRuns.WithLabelValues("unrecoverable").Inc()
	}
	return a.writeAudit(ctx, tenantID, model.AuditEventTenantReconcileUnrecoverable, map[string]any{"reason": reason})
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
