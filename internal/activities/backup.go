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
	MarkTenantBackingUpActivityName    = "MarkTenantBackingUp"
	BackupTenantDataActivityName       = "BackupTenantData"
	MarkTenantBackedUpActivityName     = "MarkTenantBackedUp"
	MarkTenantBackupFailedActivityName = "MarkTenantBackupFailed"
)

// BackupActivities wraps the steps behind taking and verifying a tenant backup.
type BackupActivities struct {
	repo      repository.BackupRepository
	auditRepo repository.AuditRepository
	provider  cloud.CloudProvider
}

func NewBackupActivities(repo repository.BackupRepository, auditRepo repository.AuditRepository, provider cloud.CloudProvider) *BackupActivities {
	return &BackupActivities{repo: repo, auditRepo: auditRepo, provider: provider}
}

// tempDBName returns the throwaway database used to prove a backup restore is
// sound (tenant_<id>_temp). It is created, restored into, validated, and
// dropped inside BackupTenantData.
func tempDBName(tenantID string) string {
	return "tenant_" + tenantID + "_temp"
}

func (a *BackupActivities) MarkTenantBackingUp(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant as backing up", "tenantID", tenantID)
	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantBackingUp,
		Actor:     "workflow",
		Payload:   map[string]any{},
	})
}

// BackupTenantData takes a point-in-time snapshot of the live DB and, crucially,
// *verifies* it is restorable: it restores the dump into a throwaway _temp DB,
// validates that DB is healthy, then drops it. Only a backup that provably
// replays cleanly is recorded as completed. On any failure the backup record
// (if created) is marked failed and the error is returned.
//
// It returns the completed backup record so the workflow can reference its ID
// and filename in the final audit event. On failure before the record is
// created it returns (nil, err); on failure after creation the record is marked
// failed but still returned alongside the error.
func (a *BackupActivities) BackupTenantData(ctx context.Context, tenantID string) (*model.Backup, error) {
	log := activity.GetLogger(ctx)
	log.Info("Backing up tenant data", "tenantID", tenantID)

	backupName, err := a.provider.SnapshotDatabase(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("snapshot tenant %s: %w", tenantID, err)
	}

	// Record that a backup artifact now exists (pending until verified).
	rec, err := a.repo.CreateBackup(ctx, &model.Backup{
		TenantID: tenantID,
		Filename: backupName,
	})
	if err != nil {
		return nil, fmt.Errorf("record backup for tenant %s: %w", tenantID, err)
	}

	// Verify the dump is restorable before trusting it.
	tempDB := tempDBName(tenantID)
	if err := a.provider.CreateDatabaseNamed(ctx, tempDB); err != nil {
		a.markFailed(ctx, rec.ID)
		return rec, fmt.Errorf("create temp database for backup verification: %w", err)
	}

	if err := a.provider.RestoreDatabaseFromBackup(ctx, tempDB, backupName); err != nil {
		a.dropTemp(ctx, tempDB)
		a.markFailed(ctx, rec.ID)
		return rec, fmt.Errorf("restore backup into temp database: %w", err)
	}

	if err := a.provider.ValidateDatabase(ctx, tempDB); err != nil {
		a.dropTemp(ctx, tempDB)
		a.markFailed(ctx, rec.ID)
		return rec, fmt.Errorf("validate restored temp database: %w", err)
	}

	// Clean up the throwaway DB and bless the backup as verified/complete.
	if err := a.provider.DropDatabaseNamed(ctx, tempDB); err != nil {
		a.markFailed(ctx, rec.ID)
		return rec, fmt.Errorf("drop temp database: %w", err)
	}

	if err := a.repo.MarkBackupCompleted(ctx, rec.ID); err != nil {
		a.markFailed(ctx, rec.ID)
		return rec, fmt.Errorf("mark backup %d completed: %w", rec.ID, err)
	}

	rec.Status = model.BackupStatusCompleted
	return rec, nil
}

func (a *BackupActivities) MarkTenantBackedUp(ctx context.Context, tenantID string, backupID int64, filename string) error {
	activity.GetLogger(ctx).Info("Marking tenant as backed up", "tenantID", tenantID, "backupID", backupID)
	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantBackupCreated,
		Actor:     "workflow",
		Payload: map[string]any{
			"backupID": backupID,
			"filename": filename,
		},
	})
}

// MarkTenantBackupFailed is the saga's deterministic compensation: it records
// that the backup did not complete on the timeline. Any created backup record
// was already marked failed inside BackupTenantData.
func (a *BackupActivities) MarkTenantBackupFailed(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant backup as failed", "tenantID", tenantID)
	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantBackupFailed,
		Actor:     "workflow",
		Payload:   map[string]any{"reason": "saga failed"},
	})
}

func (a *BackupActivities) markFailed(ctx context.Context, id int64) {
	if err := a.repo.MarkBackupFailed(ctx, id); err != nil {
		activity.GetLogger(ctx).Error("mark backup failed", "backupID", id, "error", err)
	}
}

func (a *BackupActivities) dropTemp(ctx context.Context, dbName string) {
	if err := a.provider.DropDatabaseNamed(ctx, dbName); err != nil {
		activity.GetLogger(ctx).Error("drop temp database", "database", dbName, "error", err)
	}
}
