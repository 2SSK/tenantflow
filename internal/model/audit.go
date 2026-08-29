package model

import "time"

type AuditEventType string

const (
	AuditEventTenantCreated       AuditEventType = "TENANT_CREATED"
	AuditEventTenantProvisioned   AuditEventType = "TENANT_PROVISIONED"
	AuditEventTenantActivated     AuditEventType = "TENANT_ACTIVATED"
	AuditEventTenantFailed        AuditEventType = "TENANT_FAILED"
	AuditEventTenantDeleting      AuditEventType = "TENANT_DELETING"
	AuditEventTenantDeprovisioned AuditEventType = "TENANT_DEPROVISIONED"
	AuditEventTenantDeleted       AuditEventType = "TENANT_DELETED"
	AuditEventTenantUpgrading     AuditEventType = "TENANT_UPGRADING"
	AuditEventTenantUpgraded      AuditEventType = "TENANT_UPGRADED"
	AuditEventTenantUpgradeFailed AuditEventType = "TENANT_UPGRADE_FAILED"
	AuditEventTenantQuotaRolled   AuditEventType = "TENANT_QUOTA_ROLLED_BACK"
	AuditEventTenantMigrating     AuditEventType = "TENANT_MIGRATING"
	AuditEventTenantMigrated      AuditEventType = "TENANT_MIGRATED"
	AuditEventTenantMigrateFailed AuditEventType = "TENANT_MIGRATE_FAILED"
	AuditEventTenantBackingUp     AuditEventType = "TENANT_BACKING_UP"
	AuditEventTenantBackupCreated AuditEventType = "TENANT_BACKUP_CREATED"
	AuditEventTenantBackupFailed  AuditEventType = "TENANT_BACKUP_FAILED"
	AuditEventTenantRestoring     AuditEventType = "TENANT_RESTORING"
	AuditEventTenantRestored      AuditEventType = "TENANT_RESTORED"
	AuditEventTenantRestoreFailed AuditEventType = "TENANT_RESTORE_FAILED"
)

type AuditEvent struct {
	ID         int64
	TenantID   string
	WorkflowID *string
	EventType  AuditEventType
	Actor      string
	Payload    map[string]any
	CreatedAt  time.Time
}
