package activities

import (
	"context"
	"errors"
	"fmt"

	"github.com/2SSK/tenantflow/internal/cloud"
	"github.com/2SSK/tenantflow/internal/model"
	"github.com/2SSK/tenantflow/internal/repository"
	"go.temporal.io/sdk/activity"
)

const (
	MarkTenantMigratingActivityName     = "MarkTenantMigrating"
	MigrateDataActivityName             = "MigrateData"
	SwitchTrafficActivityName           = "SwitchTraffic"
	MarkTenantMigratedActivityName      = "MarkTenantMigrated"
	DropTenantAuxDatabaseActivityName   = "DropTenantAuxDatabase"
	MarkTenantMigrateFailedActivityName = "MarkTenantMigrateFailed"
)

// MigrateActivities wraps the infra + audit steps behind a tenant migration.
// It needs only the audit repo (for progress events) and the cloud provider
// (for the real pg_dump/pg_restore/rename work); it never mutates tenant
// status, which stays "active" for the whole migration.
type MigrateActivities struct {
	auditRepo repository.AuditRepository
	provider  cloud.CloudProvider
}

func NewMigrateActivities(auditRepo repository.AuditRepository, provider cloud.CloudProvider) *MigrateActivities {
	return &MigrateActivities{auditRepo: auditRepo, provider: provider}
}

// newDBName returns the auxiliary database name used for a migration target
// (tenant_<id>_new). It sits alongside the live tenant_<id> DB and, once
// validated, is promoted into the live name by the switch step.
func newDBName(tenantID string) string {
	return "tenant_" + tenantID + "_new"
}

func (a *MigrateActivities) MarkTenantMigrating(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant as migrating", "tenantID", tenantID)
	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantMigrating,
		Actor:     "workflow",
		Payload:   map[string]any{},
	})
}

// MigrateData builds a brand-new database off to the side and proves it is
// sound. It returns the backup artifact name so the switch step (and any
// compensation) can reference it. On any failure it cleans up its own
// half-built _new DB before returning the error, so the live tenant DB is
// never touched.
func (a *MigrateActivities) MigrateData(ctx context.Context, tenantID string) (string, error) {
	newDB := newDBName(tenantID)
	activity.GetLogger(ctx).Info("Migrating tenant data", "tenantID", tenantID, "target", newDB)

	backupName, err := a.provider.SnapshotDatabase(ctx, tenantID)
	if err != nil {
		return "", fmt.Errorf("snapshot tenant %s: %w", tenantID, err)
	}

	if err := a.provider.CreateDatabaseNamed(ctx, newDB); err != nil {
		return "", fmt.Errorf("create new database for tenant %s: %w", tenantID, err)
	}

	if err := a.provider.RestoreDatabaseFromBackup(ctx, newDB, backupName); err != nil {
		a.dropAux(ctx, newDB)
		return "", fmt.Errorf("restore new database for tenant %s: %w", tenantID, err)
	}

	if err := a.provider.ValidateDatabase(ctx, newDB); err != nil {
		a.dropAux(ctx, newDB)
		return "", fmt.Errorf("validate new database for tenant %s: %w", tenantID, err)
	}

	return backupName, nil
}

// SwitchTraffic promotes the validated _new DB into the live tenant name.
// It first drops the stale live DB, then renames _new -> tenant_<id>.
// If the rename fails after the drop, it restores the old data from the
// backup back into the live name (self-healing) and returns an error so the
// saga still knows the switch did not complete cleanly.
func (a *MigrateActivities) SwitchTraffic(ctx context.Context, tenantID string, backupName string) error {
	newDB := newDBName(tenantID)
	liveDB := "tenant_" + tenantID
	activity.GetLogger(ctx).Info("Switching traffic to new database", "tenantID", tenantID)

	if err := a.provider.DropDatabase(ctx, tenantID); err != nil {
		return fmt.Errorf("drop live database for tenant %s: %w", tenantID, err)
	}

	if err := a.provider.RenameDatabase(ctx, newDB, liveDB); err != nil {
		// Live is gone and _new could not be promoted. Recover the old data so
		// the tenant keeps working with the pre-migration contents.
		restoreErr := a.provider.RestoreDatabaseFromBackup(ctx, liveDB, backupName)
		return errors.Join(fmt.Errorf("rename new database to %s: %w", liveDB, err), restoreErr)
	}

	return nil
}

func (a *MigrateActivities) MarkTenantMigrated(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant as migrated", "tenantID", tenantID)
	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantMigrated,
		Actor:     "workflow",
		Payload:   map[string]any{},
	})
}

// DropTenantAuxDatabase is the compensation cleanup for a migration that did
// not finish switching. It is idempotent: DROP ... IF EXISTS on the _new DB is
// a no-op if it was already renamed away or never created.
func (a *MigrateActivities) DropTenantAuxDatabase(ctx context.Context, tenantID string) error {
	newDB := newDBName(tenantID)
	activity.GetLogger(ctx).Info("Dropping auxiliary database (compensation)", "tenantID", tenantID, "database", newDB)
	return a.dropAux(ctx, newDB)
}

func (a *MigrateActivities) dropAux(ctx context.Context, dbName string) error {
	if err := a.provider.DropDatabaseNamed(ctx, dbName); err != nil {
		return fmt.Errorf("drop auxiliary database %s: %w", dbName, err)
	}
	return nil
}

// MarkTenantMigrateFailed records that the migration saga did not complete.
func (a *MigrateActivities) MarkTenantMigrateFailed(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant migration as failed", "tenantID", tenantID)
	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantMigrateFailed,
		Actor:     "workflow",
		Payload:   map[string]any{"reason": "saga failed"},
	})
}
