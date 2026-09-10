package cloud

import "context"

// DatabaseState is a read-only snapshot of a database's isolation posture,
// observed from the running server (not from control-plane records). It is
// the provider-facing half of reconciliation's "actual state".
type DatabaseState struct {
	// Exists reports whether the database currently exists.
	Exists bool
	// OwnerRole is the database owner's role name, or "" when the database
	// does not exist.
	OwnerRole string
	// PublicConnect reports whether the PUBLIC pseudo-role still holds
	// CONNECT on the database. A dedicated tenant database has it revoked.
	PublicConnect bool
}

// CloudProvider is the boundary between the control plane (workflows) and the
// real infrastructure (Docker in this MVP). Each method is one unit of
// infrastructure work, so a Temporal activity can call it, persist its result,
// and retry it independently.
type CloudProvider interface {
	// CreateDatabase/DropDatabase create or drop the DB derived from a tenant
	// ID (tenant_<id>). Used by provision and deprovision.
	CreateDatabase(ctx context.Context, tenantID string) error
	DropDatabase(ctx context.Context, tenantID string) error

	// DropTenantRole removes the dedicated owner role (tenant_<id>) that
	// CreateDatabase ensures for every tenant database. It is deliberately a
	// SEPARATE step from DropDatabase: migrate's SwitchTraffic drops the old
	// live DB while the tenant's new DB (owned by the same role) is being
	// promoted, so dropping the role there would orphan the promoted database.
	// Call this only at a TERMINAL teardown, after the tenant's databases are
	// gone.
	DropTenantRole(ctx context.Context, tenantID string) error

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

	// Read primitives for reconciliation (desired vs actual state):
	//
	// InspectDatabase reports whether a database exists and, if so, its owner
	// role and whether PUBLIC still holds CONNECT — the isolation posture the
	// control plane must converge back to when it drifts.
	InspectDatabase(ctx context.Context, dbName string) (DatabaseState, error)

	// RoleExists reports whether a role (e.g. a tenant's dedicated owner role)
	// currently exists. Missing role = drift, because the tenant database must
	// have a dedicated owner.
	RoleExists(ctx context.Context, roleName string) (bool, error)

	// EnsureDatabaseOwnership re-applies the tenant ownership statements for an
	// existing database: create the owner role if missing, set it as OWNER, and
	// revoke CONNECT from PUBLIC. Every statement is idempotent, so it is safe
	// to call repeatedly — this is the "repair ownership" primitive
	// reconciliation uses when the owner or PUBLIC CONNECT has drifted.
	EnsureDatabaseOwnership(ctx context.Context, dbName string) error
}
