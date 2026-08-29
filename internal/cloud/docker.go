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
	return d.execPostgres(ctx, fmt.Sprintf(`CREATE DATABASE "%s"`, dbName))
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
