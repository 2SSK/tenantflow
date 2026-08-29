package model

import "time"

type BackupStatus string

const (
	BackupStatusPending   BackupStatus = "pending"
	BackupStatusCompleted BackupStatus = "completed"
	BackupStatusFailed    BackupStatus = "failed"
)

// Backup is a control-plane record of a point-in-time database backup taken
// for a tenant. The actual artifact (a pg_dump .sql file) lives inside the
// postgres container; this row tracks *that* a backup exists so the UI can
// list history and Restore can reference a specific one.
type Backup struct {
	ID          int64
	TenantID    string
	Filename    string
	Status      BackupStatus
	CreatedAt   time.Time
	CompletedAt *time.Time
}
