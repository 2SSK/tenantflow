package model

// Drift kinds are the vocabulary of the reconciliation contract: each value
// names one way the ACTUAL state can diverge from the DESIRED state. They are
// shared by drift detection (activities), the audit payload, and the
// Prometheus drift metric label.
const (
	// DriftMissingDatabase: a dedicated tenant's database does not exist.
	DriftMissingDatabase = "missing_database"
	// DriftMissingRole: the dedicated owner role for the tenant's database
	// does not exist (the database must have a dedicated owner).
	DriftMissingRole = "missing_role"
	// DriftWrongOwner: the database exists but is no longer owned by the
	// tenant's dedicated role (e.g. a restore or manual fix replaced it).
	DriftWrongOwner = "ownership"
	// DriftPublicConnect: PUBLIC still holds CONNECT on the database, so the
	// isolation boundary is open.
	DriftPublicConnect = "public_connect"
	// DriftMissingBackup: the backup policy (≥ 1 completed backup for
	// dedicated tenants) is not satisfied.
	DriftMissingBackup = "missing_backup"
)

// TenantSpec is the DESIRED state of a tenant as declared by the control
// plane: what infrastructure the platform promises for this tenant. It is
// derived from the tenants row (status + isolation mode) plus platform
// policy (e.g. every dedicated tenant must have a verified backup), NOT
// from probing reality. Desired vs actual is the reconciliation contract.
type TenantSpec struct {
	TenantID      string
	Status        TenantStatus
	IsolationMode IsolationMode

	// RequireDatabase is true for dedicated tenants: the plane promises a
	// database tenant_<id> with a dedicated owner role and CONNECT revoked
	// from PUBLIC. Shared tenants share the platform database, so no
	// per-tenant database is promised.
	RequireDatabase bool

	// RequireBackup is the backup policy for dedicated tenants: at least one
	// completed backup must exist at all times. (Shared tenants' data lives in
	// the platform DB, backed up as a unit; the per-tenant backup promise
	// does not apply to them.)
	RequireBackup bool
}

// DatabaseActualState is what the provider observed about one tenant database.
type DatabaseActualState struct {
	Exists bool
	// OwnerRole is the database's owner role, or "" when the database does
	// not exist.
	OwnerRole string
	// PublicConnect is true when PUBLIC still holds CONNECT on the database.
	// A dedicated tenant database must revoke CONNECT from PUBLIC; a true
	// value here means the isolation boundary has drifted open.
	PublicConnect bool
}

// ReconcileActualState is the ACTUAL state probed from real infrastructure
// (Docker/Postgres) plus the control plane's own records (backups).
type ReconcileActualState struct {
	TenantID         string
	Database         DatabaseActualState
	RoleExists       bool
	CompletedBackups int
}
