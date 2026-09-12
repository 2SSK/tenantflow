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
