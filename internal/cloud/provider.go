package cloud

import "context"

// CloudProvider is the boundary between the control plane (workflows) and the
// real infrastructure (Docker in this MVP). Each method is one unit of
// infrastructure work, so a Temporal activity can call it, persist its result,
// and retry it independently.
type CloudProvider interface {
	// CreateDatabase/DropDatabase create or drop the DB derived from a tenant
	// ID (tenant_<id>). Used by provision and deprovision.
	CreateDatabase(ctx context.Context, tenantID string) error
	DropDatabase(ctx context.Context, tenantID string) error

	// CreateDatabaseNamed/DropDatabaseNamed operate on an explicitly named DB
	// (e.g. tenant_<id>_new or tenant_<id>_temp) that migrate/backup create
	// alongside the live DB.
	CreateDatabaseNamed(ctx context.Context, dbName string) error
	DropDatabaseNamed(ctx context.Context, dbName string) error

	// SnapshotDatabase dumps tenantID's live DB to a backup artifact inside the
	// postgres container and returns the artifact's filename so it can later be
	// restored (or kept as a verified backup).
	SnapshotDatabase(ctx context.Context, tenantID string) (string, error)

	// RestoreDatabaseFromBackup restores a backup artifact (by filename) into
	// an existing target DB. Callers pick the target: the live tenant DB, a
	// <tenant>_new DB during migrate, or a <tenant>_temp DB during backup
	// verification.
	RestoreDatabaseFromBackup(ctx context.Context, targetDB string, backupName string) error

	// ValidateDatabase confirms a DB is connectable and healthy (used to prove
	// a freshly restored _new or _temp DB is sound before switching to it).
	ValidateDatabase(ctx context.Context, dbName string) error

	// RenameDatabase renames an existing DB (used by migrate to promote the
	// <tenant>_new DB into the live <tenant> name after validation).
	RenameDatabase(ctx context.Context, from string, to string) error
}
