package cloud

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// backupDir is where pg_dump artifacts live *inside the postgres container*.
const backupDir = "/tmp/tf-backups"

// safeID matches PostgreSQL identifiers quoted with "...": letters, digits,
// underscore, and hyphen (real tenant IDs like rbac-admin-final use hyphens).
// Anchored ^...$ so it must match the WHOLE string. Rejects spaces, quotes,
// semicolons and shell metacharacters to stop SQL/shell injection.
var safeID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,63}$`)

// safeFile matches backup filenames built from a tenant ID + a digits-only
// timestamp + ".sql". Letters, digits, underscore, dot and hyphen only.
var safeFile = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,80}$`)

type DockerProvider struct {
	client *client.Client
	log    *slog.Logger
}

func NewDockerProvider(log *slog.Logger) (*DockerProvider, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &DockerProvider{client: cli, log: log}, nil
}

func (d *DockerProvider) CreateDatabase(ctx context.Context, tenantID string) error {
	return d.CreateDatabaseNamed(ctx, tenantDatabaseName(tenantID))
}

func (d *DockerProvider) DropDatabase(ctx context.Context, tenantID string) error {
	return d.DropDatabaseNamed(ctx, tenantDatabaseName(tenantID))
}

func tenantDatabaseName(tenantID string) string {
	return "tenant_" + tenantID
}

// TenantDatabaseName returns the control plane's database name for a tenant
// (tenant_<id>). Non-cloud packages (reconciliation activities, tests) need
// the naming rule the provider uses so probes and reports target the exact
// database the provider manages instead of re-deriving it independently.
func TenantDatabaseName(tenantID string) string {
	return tenantDatabaseName(tenantID)
}

func validateIdentifier(id string) error {
	if !safeID.MatchString(id) {
		return fmt.Errorf("invalid identifier %q: must match %s", id, safeID.String())
	}
	return nil
}

func (d *DockerProvider) CreateDatabaseNamed(ctx context.Context, dbName string) error {
	if err := validateIdentifier(dbName); err != nil {
		return err
	}
	d.log.Info("creating database", "database", dbName)
	if err := d.execPostgres(ctx, fmt.Sprintf(`CREATE DATABASE "%s"`, dbName)); err != nil {
		return err
	}
	return d.applyDatabaseOwnership(ctx, dbName)
}

// tenantOwnerRole derives the dedicated owner role for a tenant database.
// Tenant databases are named tenant_<id> with optional auxiliary suffixes
// (_new = migrate target, _temp = backup verification); the owner role is
// ALWAYS the unsuffixed tenant_<id>. Migrate and backup therefore reuse the
// role the tenant already owns instead of minting throwaway ones.
func tenantOwnerRole(dbName string) (string, error) {
	role := dbName
	switch {
	case strings.HasSuffix(role, "_new"):
		role = strings.TrimSuffix(role, "_new")
	case strings.HasSuffix(role, "_temp"):
		role = strings.TrimSuffix(role, "_temp")
	}
	if err := validateIdentifier(role); err != nil {
		return "", fmt.Errorf("derive owner role from %q: %w", dbName, err)
	}
	return role, nil
}

// ownershipStatements returns the SQL that establishes the "database-per-tenant
// with a dedicated owner role" isolation shape for a tenant database:
//
//  1. ensure the per-tenant role exists (idempotent — DO block + IF NOT EXISTS,
//     because migrate/backup re-apply this to an already-existing role)
//  2. make that role the database OWNER
//  3. revoke CONNECT from PUBLIC so only the owner role (and superusers, who
//     bypass access checks) can connect to the tenant's database
//
// The identifiers are validated by the caller (validateIdentifier) before they
// get here, so none can contain quotes or semicolons.
func ownershipStatements(dbName string) ([]string, error) {
	if err := validateIdentifier(dbName); err != nil {
		return nil, err
	}
	role, err := tenantOwnerRole(dbName)
	if err != nil {
		return nil, err
	}
	return []string{
		fmt.Sprintf(`DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s') THEN CREATE ROLE "%s"; END IF; END $$;`, role, role),
		fmt.Sprintf(`ALTER DATABASE "%s" OWNER TO "%s"`, dbName, role),
		fmt.Sprintf(`REVOKE CONNECT ON DATABASE "%s" FROM PUBLIC`, dbName),
	}, nil
}

// applyDatabaseOwnership runs the ownership statements for a tenant database.
// They are idempotent, so this is safe both on Temporal retries and when the
// database already exists (migrate/backup create auxiliary DBs for a tenant
// whose role is already present).
func (d *DockerProvider) applyDatabaseOwnership(ctx context.Context, dbName string) error {
	stmts, err := ownershipStatements(dbName)
	if err != nil {
		return err
	}
	for _, stmt := range stmts {
		if err := d.execPostgres(ctx, stmt); err != nil {
			return fmt.Errorf("apply ownership for database %s: %w", dbName, err)
		}
	}
	return nil
}

// DropTenantRole removes the dedicated owner role after a tenant's databases
// are gone. It is idempotent (DROP ROLE IF EXISTS). See CloudProvider's
// DropTenantRole doc for why it must NOT be folded into DropDatabase.
func (d *DockerProvider) DropTenantRole(ctx context.Context, tenantID string) error {
	if err := validateIdentifier(tenantID); err != nil {
		return err
	}
	role := tenantDatabaseName(tenantID)
	d.log.Info("dropping tenant role", "role", role)
	return d.execPostgres(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, role))
}

// InspectDatabase reports the isolation posture of a database as observed
// from the running server: does it exist, who owns it, and does PUBLIC still
// hold CONNECT. This is the provider half of reconciliation's "actual state".
func (d *DockerProvider) InspectDatabase(ctx context.Context, dbName string) (DatabaseState, error) {
	if err := validateIdentifier(dbName); err != nil {
		return DatabaseState{}, err
	}
	d.log.Info("inspecting database", "database", dbName)

	state := DatabaseState{}
	out, err := d.queryPostgres(ctx, fmt.Sprintf(
		`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = '%s')`, dbName))
	if err != nil {
		return DatabaseState{}, fmt.Errorf("inspect database %s (exists): %w", dbName, err)
	}
	state.Exists = parseBoolOut(out)

	if !state.Exists {
		return state, nil
	}

	out, err = d.queryPostgres(ctx, fmt.Sprintf(
		`SELECT COALESCE(r.rolname, '') FROM pg_database d LEFT JOIN pg_roles r ON r.oid = d.datdba WHERE d.datname = '%s'`, dbName))
	if err != nil {
		return DatabaseState{}, fmt.Errorf("inspect database %s (owner): %w", dbName, err)
	}
	state.OwnerRole = out

	out, err = d.queryPostgres(ctx, fmt.Sprintf(
		`SELECT has_database_privilege('public', '%s', 'CONNECT')`, dbName))
	if err != nil {
		return DatabaseState{}, fmt.Errorf("inspect database %s (connect): %w", dbName, err)
	}
	state.PublicConnect = parseBoolOut(out)

	return state, nil
}

// RoleExists reports whether a role currently exists. Reconciliation treats a
// missing dedicated owner role as drift: the tenant database must have one.
func (d *DockerProvider) RoleExists(ctx context.Context, roleName string) (bool, error) {
	if err := validateIdentifier(roleName); err != nil {
		return false, err
	}
	d.log.Info("checking role exists", "role", roleName)
	out, err := d.queryPostgres(ctx, fmt.Sprintf(
		`SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = '%s')`, roleName))
	if err != nil {
		return false, fmt.Errorf("role exists %s: %w", roleName, err)
	}
	return parseBoolOut(out), nil
}

// EnsureDatabaseOwnership re-applies the tenant ownership statements to an
// existing database: create the owner role if missing, set it as OWNER, and
// revoke CONNECT from PUBLIC. Idempotent by construction, so it is the
// "repair ownership" primitive reconciliation calls when the owner role or
// PUBLIC CONNECT has drifted.
func (d *DockerProvider) EnsureDatabaseOwnership(ctx context.Context, dbName string) error {
	if err := validateIdentifier(dbName); err != nil {
		return err
	}
	d.log.Info("ensuring database ownership", "database", dbName)
	return d.applyDatabaseOwnership(ctx, dbName)
}

func (d *DockerProvider) DropDatabaseNamed(ctx context.Context, dbName string) error {
	if err := validateIdentifier(dbName); err != nil {
		return err
	}
	d.log.Info("dropping database", "database", dbName)
	// Terminate open connections first; DROP DATABASE fails if anyone is connected.
	d.execPostgres(ctx, fmt.Sprintf(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s'`, dbName))
	return d.execPostgres(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, dbName))
}

func (d *DockerProvider) SnapshotDatabase(ctx context.Context, tenantID string) (string, error) {
	if err := validateIdentifier(tenantID); err != nil {
		return "", err
	}
	dbName := tenantDatabaseName(tenantID)
	backupName := fmt.Sprintf("%s_%s.sql", tenantID, time.Now().Format("20060102-150405"))
	d.log.Info("snapshotting tenant database", "database", dbName, "backup", backupName)

	script := fmt.Sprintf(`mkdir -p %s && pg_dump -U temporal "%s" -f %s/%s`, backupDir, dbName, backupDir, backupName)
	if err := d.execShell(ctx, script); err != nil {
		return "", fmt.Errorf("snapshot database %s: %w", dbName, err)
	}
	return backupName, nil
}

func (d *DockerProvider) RestoreDatabaseFromBackup(ctx context.Context, targetDB string, backupName string) error {
	if err := validateIdentifier(targetDB); err != nil {
		return err
	}
	if !safeFile.MatchString(backupName) {
		return fmt.Errorf("invalid backup name %q", backupName)
	}
	d.log.Info("restoring database from backup", "database", targetDB, "backup", backupName)

	script := fmt.Sprintf(`psql -U temporal -d "%s" -v ON_ERROR_STOP=1 -f %s/%s`, targetDB, backupDir, backupName)
	if err := d.execShell(ctx, script); err != nil {
		return fmt.Errorf("restore database %s from backup %s: %w", targetDB, backupName, err)
	}
	return nil
}

func (d *DockerProvider) ValidateDatabase(ctx context.Context, dbName string) error {
	if err := validateIdentifier(dbName); err != nil {
		return err
	}
	d.log.Info("validating database", "database", dbName)

	// Count user tables as a lightweight health/proof-of-restore check. A
	// freshly restored _new or _temp DB must be connectable and properly
	// shaped; this SELECT fails fast if the DB is missing or broken.
	script := fmt.Sprintf(
		`psql -U temporal -d "%s" -t -A -c "SELECT count(*) FROM pg_stat_user_tables"`,
		dbName,
	)
	if err := d.execShell(ctx, script); err != nil {
		return fmt.Errorf("validate database %s: %w", dbName, err)
	}
	return nil
}

func (d *DockerProvider) RenameDatabase(ctx context.Context, from string, to string) error {
	if err := validateIdentifier(from); err != nil {
		return err
	}
	if err := validateIdentifier(to); err != nil {
		return err
	}
	d.log.Info("renaming database", "from", from, "to", to)

	// ALTER DATABASE cannot run inside a transaction block and the DB being
	// renamed must have no open connections. Unqualified "postgres -c" runs
	// each statement in its own transaction, which is permitted.
	return d.execPostgres(ctx, fmt.Sprintf(`ALTER DATABASE "%s" RENAME TO "%s"`, from, to))
}

func (d *DockerProvider) execShell(ctx context.Context, script string) error {
	postgresID, err := d.findPostgresContainer(ctx)
	if err != nil {
		return err
	}
	execResp, err := d.client.ContainerExecCreate(ctx, postgresID, container.ExecOptions{
		Cmd:          []string{"bash", "-lc", script},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}
	if err := d.client.ContainerExecStart(ctx, execResp.ID, container.ExecStartOptions{}); err != nil {
		return fmt.Errorf("exec start: %w", err)
	}
	inspect, err := d.client.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("command in postgres container failed with exit code %d: %s", inspect.ExitCode, script)
	}
	return nil
}

func (d *DockerProvider) execPostgres(ctx context.Context, sql string) error {
	postgresID, err := d.findPostgresContainer(ctx)
	if err != nil {
		return err
	}
	execResp, err := d.client.ContainerExecCreate(ctx, postgresID, container.ExecOptions{
		Cmd:          []string{"psql", "-U", "temporal", "-c", sql},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}
	if err := d.client.ContainerExecStart(ctx, execResp.ID, container.ExecStartOptions{}); err != nil {
		return fmt.Errorf("exec start: %w", err)
	}
	inspect, err := d.client.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("psql exited with code %d running: %s", inspect.ExitCode, sql)
	}
	return nil
}

// queryPostgres runs a single statement via psql with -t -A (tuples only,
// unaligned) and returns the trimmed output — used for scalar reads like
// EXISTS(...) and COALESCE(...). Unlike execPostgres it ATTACHES to the exec
// so psql's output is actually captured instead of discarded. The hijacked
// stream MULTIPLEXES stdout and stderr (8-byte frame headers per write), so
// it is demuxed with stdcopy: stdout carries the query result, stderr carries
// psql errors. Callers must pre-validate any interpolated identifiers
// (validateIdentifier) so no value can break out of the SQL string.
func (d *DockerProvider) queryPostgres(ctx context.Context, sql string) (string, error) {
	postgresID, err := d.findPostgresContainer(ctx)
	if err != nil {
		return "", err
	}
	execResp, err := d.client.ContainerExecCreate(ctx, postgresID, container.ExecOptions{
		Cmd:          []string{"psql", "-U", "temporal", "-t", "-A", "-c", sql},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return "", fmt.Errorf("exec create: %w", err)
	}
	resp, err := d.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("exec attach: %w", err)
	}
	defer resp.Close()

	var stdout, stderr strings.Builder
	if _, err := stdcopy.StdCopy(&stdout, &stderr, resp.Reader); err != nil {
		return "", fmt.Errorf("exec read: %w", err)
	}
	inspect, err := d.client.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return "", fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return "", fmt.Errorf("psql exited with code %d running: %s: %s",
			inspect.ExitCode, sql, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// parseBoolOut interprets psql -t -A boolean output ("t"/"f") and the
// common textual variants of booleans defensively.
func parseBoolOut(out string) bool {
	switch strings.ToLower(strings.TrimSpace(out)) {
	case "t", "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

// findPostgresContainer returns the ID of the running postgres container.
func (d *DockerProvider) findPostgresContainer(ctx context.Context) (string, error) {
	containers, err := d.client.ContainerList(ctx, container.ListOptions{All: false})
	if err != nil {
		return "", fmt.Errorf("list containers: %w", err)
	}
	for _, c := range containers {
		for _, name := range c.Names {
			if strings.Contains(name, "postgres") {
				return c.ID, nil
			}
		}
	}
	return "", fmt.Errorf("no running postgres container found")
}
