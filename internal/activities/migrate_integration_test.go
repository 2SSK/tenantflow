//go:build integration

package activities

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/2SSK/tenantflow/internal/cloud"
	"github.com/2SSK/tenantflow/internal/model"
	"github.com/2SSK/tenantflow/internal/repository"
)

// iprovider connects to the real platform postgres container, exactly like the
// cloud provider integration tests do. These tests exercise the activity
// retry semantics (the temporal kill/re-run window) end to end against the
// real database the activities drive.
func iprovider(t *testing.T) *cloud.DockerProvider {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, idatabaseURL("tenantflow"))
	if err != nil {
		t.Fatalf("connect to platform database: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping platform database: %v", err)
	}
	p, err := cloud.NewDockerProvider(log)
	if err != nil {
		t.Fatalf("NewDockerProvider: %v", err)
	}
	return p
}

func idatabaseURL(dbName string) string {
	if url := os.Getenv("TENANTFLOW_DATABASE_URL"); url != "" && dbName == "tenantflow" {
		return url
	}
	return fmt.Sprintf("postgres://temporal:temporal@localhost:5433/%s?sslmode=disable", dbName)
}

func itenantID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// ptr returns a pointer to v; Go lacks a built-in, and the Tenant.WorkflowID
// field is a nullable *string.
func ptr[T any](v T) *T { return &v }

// TestMigrateDataResumesAfterStaleNewDB simulates the Temporal retry window:
// a previous MigrateData attempt crashed AFTER creating the fixed-name _new DB
// but BEFORE building it, leaving a stale tenant_<id>_new behind. Before the
// idempotency fix this made the retry fail with "database already exists",
// sending the migration into the DLQ. It must now pre-drop and rebuild.
func TestMigrateDataResumesAfterStaleNewDB(t *testing.T) {
	ctx := context.Background()
	p := iprovider(t)

	tenantID := itenantID("itmig-stale")
	live := "tenant_" + tenantID
	newDB := live + "_new"

	if err := p.CreateDatabase(ctx, tenantID); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	// Crash residue: the _new DB from the previous, interrupted attempt.
	if err := p.CreateDatabaseNamed(ctx, newDB); err != nil {
		t.Fatalf("seed stale _new: %v", err)
	}
	t.Cleanup(func() {
		_ = p.DropDatabaseNamed(ctx, newDB)
		_ = p.DropDatabase(ctx, tenantID)
		_ = p.DropTenantRole(ctx, tenantID)
	})

	m := NewMigrateActivities(nil, p)
	backupName, err := m.MigrateData(ctx, tenantID)
	if err != nil {
		t.Fatalf("MigrateData with stale _new present: %v", err)
	}
	if backupName == "" {
		t.Fatal("MigrateData returned empty backup name")
	}

	st, err := p.InspectDatabase(ctx, newDB)
	if err != nil {
		t.Fatalf("inspect rebuilt _new: %v", err)
	}
	if !st.Exists {
		t.Fatal("_new does not exist after MigrateData; the stale DB was not rebuilt")
	}
	if st.OwnerRole != live {
		t.Errorf("rebuilt _new owner = %q, want %q (dedicated tenant role)", st.OwnerRole, live)
	}
	if st.PublicConnect {
		t.Errorf("rebuilt _new still grants PUBLIC CONNECT; isolation not reapplied")
	}
}

// TestSwitchTrafficRetryIsIdempotent simulates the dangerous retry window:
// attempt 1 COMPLETED the switch (renamed _new -> live) but the worker died
// before the activity reported success. A naive retry would re-run
// "drop live" and DESTROY the freshly promoted database. The activity must
// recognize _new is gone and no-op successfully, leaving live untouched.
func TestSwitchTrafficRetryIsIdempotent(t *testing.T) {
	ctx := context.Background()
	p := iprovider(t)

	tenantID := itenantID("itsw-retry")
	live := "tenant_" + tenantID
	newDB := live + "_new"

	if err := p.CreateDatabase(ctx, tenantID); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if err := p.CreateDatabaseNamed(ctx, newDB); err != nil {
		t.Fatalf("CreateDatabaseNamed(_new): %v", err)
	}
	t.Cleanup(func() {
		_ = p.DropDatabase(ctx, tenantID)
		_ = p.DropTenantRole(ctx, tenantID)
	})

	// Simulate attempt 1 having fully switched: live is replaced by _new.
	if err := p.DropDatabase(ctx, tenantID); err != nil {
		t.Fatalf("simulate dropped live: %v", err)
	}
	if err := p.RenameDatabase(ctx, newDB, live); err != nil {
		t.Fatalf("simulate promoted _new: %v", err)
	}

	m := NewMigrateActivities(nil, p)
	if err := m.SwitchTraffic(ctx, tenantID, "unused-backup.sql"); err != nil {
		t.Fatalf("SwitchTraffic retry after completed switch: %v", err)
	}

	st, err := p.InspectDatabase(ctx, live)
	if err != nil {
		t.Fatalf("inspect live: %v", err)
	}
	if !st.Exists {
		t.Fatal("live database was destroyed by the retried switch; promoted data lost")
	}
	if st2, _ := p.InspectDatabase(ctx, newDB); st2.Exists {
		t.Fatal("_new still exists after switch already completed; state inconsistent")
	}
}

// TestSwitchTrafficCompletesInterruptedSwitch simulates the OTHER retry
// window: attempt 1 crashed right after dropping live, before renaming _new.
// The retry must finish the promotion (not treat _new as absent).
func TestSwitchTrafficCompletesInterruptedSwitch(t *testing.T) {
	ctx := context.Background()
	p := iprovider(t)

	tenantID := itenantID("itsw-mid")
	live := "tenant_" + tenantID
	newDB := live + "_new"

	if err := p.CreateDatabase(ctx, tenantID); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if err := p.CreateDatabaseNamed(ctx, newDB); err != nil {
		t.Fatalf("CreateDatabaseNamed(_new): %v", err)
	}
	t.Cleanup(func() {
		_ = p.DropDatabase(ctx, tenantID)
		_ = p.DropTenantRole(ctx, tenantID)
	})

	// Crash residue: live was dropped, _new was NOT yet renamed.
	if err := p.DropDatabase(ctx, tenantID); err != nil {
		t.Fatalf("simulate crash after live drop: %v", err)
	}

	m := NewMigrateActivities(nil, p)
	if err := m.SwitchTraffic(ctx, tenantID, "unused-backup.sql"); err != nil {
		t.Fatalf("SwitchTraffic retry mid-switch: %v", err)
	}

	st, err := p.InspectDatabase(ctx, live)
	if err != nil {
		t.Fatalf("inspect live: %v", err)
	}
	if !st.Exists {
		t.Fatal("switch was not completed by the retry; live missing after mid-switch retry")
	}
	if st2, _ := p.InspectDatabase(ctx, newDB); st2.Exists {
		t.Fatal("_new still exists; promotion did not happen")
	}
}

func realAuditRepo(t *testing.T) repository.AuditRepository {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), idatabaseURL("tenantflow"))
	if err != nil {
		t.Fatalf("connect audit repo: %v", err)
	}
	t.Cleanup(pool.Close)
	return repository.NewPostgresAuditRepository(pool)
}

// TestProvisionTenantResumesAfterPartialCreate simulates the retry window of
// the most common activity: attempt 1 created the tenant database but crashed
// before reporting success. A naive retry died with "database already exists"
// and the new tenant was stuck in the DLQ until a human dropped the orphan DB.
// ProvisionTenant must now converge: keep the existing DB and re-assert the
// isolation posture instead of failing.
func TestProvisionTenantResumesAfterPartialCreate(t *testing.T) {
	ctx := context.Background()
	p := iprovider(t)

	tenantID := itenantID("itprov-resume")
	live := "tenant_" + tenantID

	if err := p.CreateDatabase(ctx, tenantID); err != nil {
		t.Fatalf("simulate partial create residue: %v", err)
	}
	t.Cleanup(func() {
		_ = p.DropDatabase(ctx, tenantID)
		_ = p.DropTenantRole(ctx, tenantID)
	})

	// Simulate attempt 1's earlier steps: the tenant record is already on file
	// (audit events FK to it) and the database exists, but the activity crashed
	// before reporting success.
	seedPool, err := pgxpool.New(ctx, idatabaseURL("tenantflow"))
	if err != nil {
		t.Fatalf("connect for tenant record: %v", err)
	}
	seedTenants := repository.NewPostgresTenantRepository(seedPool)
	if err := seedTenants.CreateTenant(ctx, &model.Tenant{
		TenantID:      tenantID,
		Status:        model.TenantStatusProvisioning,
		WorkflowID:    ptr("provision-" + tenantID),
		IsolationMode: model.IsolationModeDedicated,
	}); err != nil {
		t.Fatalf("seed tenant record: %v", err)
	}

	// Sabotage the ownership posture so the test proves the converge step
	// actually re-asserts isolation, not just skips when the DB exists.
	if _, err := seedPool.Exec(ctx, `ALTER DATABASE "`+live+`" OWNER TO temporal`); err != nil {
		t.Fatalf("sabotage owner: %v", err)
	}
	seedPool.Close()

	act := NewProvisionActivities(seedTenants, realAuditRepo(t), p)
	if err := act.ProvisionTenant(ctx, tenantID, string(model.IsolationModeDedicated)); err != nil {
		t.Fatalf("ProvisionTenant retry after partial create: %v", err)
	}

	st, err := p.InspectDatabase(ctx, live)
	if err != nil {
		t.Fatalf("inspect live: %v", err)
	}
	if !st.Exists {
		t.Fatal("live database missing after provision retry")
	}
	if st.OwnerRole != live {
		t.Errorf("owner = %q, want %q (ownership was not re-asserted)", st.OwnerRole, live)
	}
	if st.PublicConnect {
		t.Errorf("PUBLIC can still CONNECT; isolation posture not re-applied")
	}
}

// TestRestoreTenantFromBackup_DropsResidueBeforeCreate is the end-to-end
// version of the unit test: it simulates the Temporal retry window against
// the real postgres container and proves convergence.
//
// Sequence:
//  1. seed tenant_<id> with proof_table + row, snapshot it → the verified backup
//  2. drop the database (the DriftMissingDatabase data-loss event)
//  3. recreate tenant_<id> with only residue_table (the crashed attempt's residue)
//  4. record the backup as completed in the real backup repo
//  5. run RestoreTenantFromBackup inside a Temporal activity env
//  6. assert proof_table came back from the verified backup — and
//     residue_table is GONE, proving the pre-drop destroyed the partial state
func TestRestoreTenantFromBackup_DropsResidueBeforeCreate(t *testing.T) {
	ctx := context.Background()
	p := iprovider(t)
	tenantID := itenantID("itres-drop")
	live := cloud.TenantDatabaseName(tenantID)

	platformPool, err := pgxpool.New(ctx, idatabaseURL("tenantflow"))
	if err != nil {
		t.Fatalf("connect to platform database: %v", err)
	}
	defer platformPool.Close()

	// Audit events FK to tenants, so the row must exist before the activity
	// writes its event. Provisioning status keeps the test tenant invisible
	// to the live reconcile sweep, which skips non-active tenants.
	seedTenants := repository.NewPostgresTenantRepository(platformPool)
	if err := seedTenants.CreateTenant(ctx, &model.Tenant{
		TenantID:      tenantID,
		Status:        model.TenantStatusProvisioning,
		WorkflowID:    ptr("reconcile-" + tenantID),
		IsolationMode: model.IsolationModeDedicated,
	}); err != nil {
		t.Fatalf("seed tenant record: %v", err)
	}
	t.Cleanup(func() {
		_ = p.DropDatabase(ctx, tenantID)
		_ = p.DropTenantRole(ctx, tenantID)
	})

	// Step 1 — the pre-crash database with the data we want back. The tenant
	// DB is isolated (PUBLIC CONNECT revoked), so the seed connects to it
	// directly as the superuser rather than crossing databases (PostgreSQL
	// has no cross-database queries).
	if err := p.CreateDatabase(ctx, tenantID); err != nil {
		t.Fatalf("create seed database: %v", err)
	}
	seedPool, err := pgxpool.New(ctx, idatabaseURL(live))
	if err != nil {
		t.Fatalf("connect to seed database: %v", err)
	}
	if _, err := seedPool.Exec(ctx, `CREATE TABLE proof_table (id integer)`); err != nil {
		t.Fatalf("create proof table: %v", err)
	}
	if _, err := seedPool.Exec(ctx, `INSERT INTO proof_table VALUES (1)`); err != nil {
		t.Fatalf("insert proof row: %v", err)
	}
	seedPool.Close()
	dumpName, err := p.SnapshotDatabase(ctx, tenantID)
	if err != nil {
		t.Fatalf("snapshot seed database: %v", err)
	}

	// Step 2 — data loss: the database goes missing.
	if err := p.DropDatabase(ctx, tenantID); err != nil {
		t.Fatalf("drop database (data-loss event): %v", err)
	}

	// Step 3 — crash residue: a retried attempt created the DB and got partway
	// through the restore (residue_table only; proof_table is still absent).
	if err := p.CreateDatabase(ctx, tenantID); err != nil {
		t.Fatalf("recreate database as crash residue: %v", err)
	}
	residuePool, err := pgxpool.New(ctx, idatabaseURL(live))
	if err != nil {
		t.Fatalf("connect to residue database: %v", err)
	}
	if _, err := residuePool.Exec(ctx, `CREATE TABLE residue_table (id integer)`); err != nil {
		t.Fatalf("create residue table: %v", err)
	}
	residuePool.Close()

	// Step 4 — the verified backup is on record.
	backupRepo := repository.NewPostgresBackupRepository(platformPool)
	created, err := backupRepo.CreateBackup(ctx, &model.Backup{TenantID: tenantID, Filename: dumpName})
	if err != nil {
		t.Fatalf("seed backup record: %v", err)
	}
	if err := backupRepo.MarkBackupCompleted(ctx, created.ID); err != nil {
		t.Fatalf("mark backup completed: %v", err)
	}

	// Step 5 — run the activity (the audit write requires a Temporal env).
	act := NewReconcileActivities(nil, realAuditRepo(t), backupRepo, p, nil)
	if err := runRestore(t, act, tenantID); err != nil {
		t.Fatalf("RestoreTenantFromBackup retry after crash residue: %v", err)
	}

	// Step 6 — convergence: the isolation shape is re-applied, proof_table
	// came back from the verified backup…
	st, err := p.InspectDatabase(ctx, live)
	if err != nil {
		t.Fatalf("inspect live: %v", err)
	}
	if !st.Exists {
		t.Fatal("live database missing after restore retry")
	}
	if st.OwnerRole != live {
		t.Errorf("owner = %q, want %q (ownership was not re-asserted)", st.OwnerRole, live)
	}
	if st.PublicConnect {
		t.Errorf("PUBLIC can still CONNECT; isolation posture not re-applied")
	}
	verifyPool, err := pgxpool.New(ctx, idatabaseURL(live))
	if err != nil {
		t.Fatalf("connect to restored database: %v", err)
	}
	defer verifyPool.Close()
	var n int
	if err := verifyPool.QueryRow(ctx, `SELECT count(*) FROM proof_table`).Scan(&n); err != nil {
		t.Fatalf("query proof_table after restore: %v", err)
	}
	if n != 1 {
		t.Errorf("proof_table rows = %d, want 1 (verified backup was not restored)", n)
	}
	// …and residue_table is GONE, proving the pre-drop removed the crashed
	// attempt's partial state instead of restoring on top of it.
	var m int
	if err := verifyPool.QueryRow(ctx, `SELECT count(*) FROM residue_table`).Scan(&m); err == nil {
		t.Errorf("residue_table still exists (rows=%d) after restore; the pre-drop did not fire", m)
	}
}
