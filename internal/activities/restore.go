package activities

import (
	"context"
	"fmt"

	"github.com/2SSK/tenantflow/internal/cloud"
	"github.com/2SSK/tenantflow/internal/model"
	"github.com/2SSK/tenantflow/internal/repository"
	"go.temporal.io/sdk/activity"
)

const (
	MarkTenantRestoringActivityName     = "MarkTenantRestoring"
	PreRestoreSnapshotActivityName      = "PreRestoreSnapshot"
	RestoreDataActivityName             = "RestoreData"
	MarkTenantRestoredActivityName      = "MarkTenantRestored"
	RestoreRollbackActivityName         = "RestoreRollback"
	MarkTenantRestoreFailedActivityName = "MarkTenantRestoreFailed"
)

// RestoreActivities wraps the steps behind restoring a tenant's database from a
// previously taken backup.
type RestoreActivities struct {
	repo      repository.BackupRepository
	auditRepo repository.AuditRepository
	provider  cloud.CloudProvider
}

func NewRestoreActivities(repo repository.BackupRepository, auditRepo repository.AuditRepository, provider cloud.CloudProvider) *RestoreActivities {
	return &RestoreActivities{repo: repo, auditRepo: auditRepo, provider: provider}
}

// tenantDBName returns the live database name for a tenant (tenant_<id>),
// matching the provisioning convention.
func tenantDBName(tenantID string) string {
	return "tenant_" + tenantID
}

func (a *RestoreActivities) MarkTenantRestoring(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant as restoring", "tenantID", tenantID)
	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantRestoring,
		Actor:     "workflow",
		Payload:   map[string]any{},
	})
}

// PreRestoreSnapshot takes a safety snapshot of the CURRENT live database before
// any restore overwrites it. This is the rollback point: if the restore fails,
// the saga restores from this snapshot to bring the tenant back to its
// pre-restore state. It returns the generated backup filename so the workflow
// can pass it to the compensation. (The snapshot is ephemeral - it exists to
// unwind a bad restore, not to be listed as a durable backup.)
func (a *RestoreActivities) PreRestoreSnapshot(ctx context.Context, tenantID string) (string, error) {
	activity.GetLogger(ctx).Info("Taking pre-restore safety snapshot", "tenantID", tenantID)
	return a.provider.SnapshotDatabase(ctx, tenantID)
}

// RestoreData overwrites the live tenant DB with the contents of the given
// backup record, then validates the result. Lookup happens by backup ID so the
// UI can reference a specific historical backup. On success the live DB holds
// the restored data; on failure the live DB may be partially overwritten, which
// is why the saga keeps the pre-restore snapshot to roll back with.
func (a *RestoreActivities) RestoreData(ctx context.Context, tenantID string, backupID int64) error {
	log := activity.GetLogger(ctx)
	log.Info("Restoring tenant data", "tenantID", tenantID, "backupID", backupID)

	backup, err := a.repo.GetBackup(ctx, backupID)
	if err != nil {
		return fmt.Errorf("get backup %d: %w", backupID, err)
	}
	if backup.TenantID != tenantID {
		return fmt.Errorf("backup %d belongs to tenant %q, not %q", backupID, backup.TenantID, tenantID)
	}

	if err := a.provider.RestoreDatabaseFromBackup(ctx, tenantDBName(tenantID), backup.Filename); err != nil {
		return fmt.Errorf("restore backup %d into tenant db: %w", backupID, err)
	}

	if err := a.provider.ValidateDatabase(ctx, tenantDBName(tenantID)); err != nil {
		return fmt.Errorf("validate restored tenant db: %w", err)
	}

	return nil
}

func (a *RestoreActivities) MarkTenantRestored(ctx context.Context, tenantID string, backupID int64) error {
	activity.GetLogger(ctx).Info("Marking tenant as restored", "tenantID", tenantID, "backupID", backupID)
	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantRestored,
		Actor:     "workflow",
		Payload:   map[string]any{"backupID": backupID},
	})
}

// RestoreRollback is the saga's compensation: it restores the pre-restore
// safety snapshot back into the live tenant DB, undoing any partial or complete
// restore that happened after the snapshot was taken. It is invoked whenever the
// workflow fails after a pre-restore snapshot was captured, because the only way
// to be sure the live DB is back to its pre-restore state is to overwrite it
// with that snapshot.
func (a *RestoreActivities) RestoreRollback(ctx context.Context, tenantID, preBackupName string) error {
	log := activity.GetLogger(ctx)
	log.Info("Rolling back restore from pre-restore snapshot", "tenantID", tenantID, "preBackupName", preBackupName)
	if err := a.provider.RestoreDatabaseFromBackup(ctx, tenantDBName(tenantID), preBackupName); err != nil {
		return fmt.Errorf("rollback restore for tenant %s: %w", tenantID, err)
	}

	// The rollback overwrote the live DB with the safety snapshot: record it
	// so the compensation history shows exactly when and why the restore was
	// undone.
	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantRestoreRolledBack,
		Actor:     "workflow",
		Payload:   compensationEvent("RestoreRollback", "saga compensation"),
	})
}

func (a *RestoreActivities) MarkTenantRestoreFailed(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant restore as failed", "tenantID", tenantID)
	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantRestoreFailed,
		Actor:     "workflow",
		Payload:   map[string]any{"reason": "saga failed"},
	})
}
