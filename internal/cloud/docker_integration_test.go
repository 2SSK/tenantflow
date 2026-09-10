//go:build integration

package cloud

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool connects to the real platform postgres, exactly like the
// repository integration tests do. The DockerProvider exercises the same
// container, so this test asserts the full chain: provider SQL -> postgres.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TENANTFLOW_DATABASE_URL")
	if url == "" {
		url = "postgres://temporal:temporal@localhost:5433/tenantflow?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func integrationProvider(t *testing.T) *DockerProvider {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := NewDockerProvider(log)
	if err != nil {
		t.Fatalf("NewDockerProvider: %v", err)
	}
	return p
}

func uniqueTenantID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// databaseOwner returns the rolname of a database's owner, or "" if the
// database does not exist.
func databaseOwner(ctx context.Context, pool *pgxpool.Pool, dbName string) string {
	var owner string
	err := pool.QueryRow(ctx,
		`SELECT r.rolname FROM pg_database d JOIN pg_roles r ON r.oid = d.datdba WHERE d.datname = $1`,
		dbName).Scan(&owner)
	if err != nil {
		return ""
	}
	return owner
}

func publicCanConnect(t *testing.T, ctx context.Context, pool *pgxpool.Pool, dbName string) bool {
	t.Helper()
	var priv bool
	if err := pool.QueryRow(ctx,
		`SELECT pg_catalog.has_database_privilege('public', $1, 'CONNECT')`, dbName).Scan(&priv); err != nil {
		t.Fatalf("has_database_privilege(%s): %v", dbName, err)
	}
	return priv
}

func TestCreateDatabaseEstablishesOwnership(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	provider := integrationProvider(t)

	tenantID := uniqueTenantID("it-owner")
	live := "tenant_" + tenantID
	role := live

	if err := provider.CreateDatabase(ctx, tenantID); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	t.Cleanup(func() {
		_ = provider.DropDatabase(ctx, tenantID)
		_ = provider.DropTenantRole(ctx, tenantID)
	})

	if got := databaseOwner(ctx, pool, live); got != role {
		t.Errorf("owner of %s = %q, want %q (per-tenant owner role)", live, got, role)
	}
	if publicCanConnect(t, ctx, pool, live) {
		t.Errorf("PUBLIC can still CONNECT to %s; REVOKE CONNECT failed", live)
	}

	// A migration target born alongside the live DB must reuse the SAME role,
	// not create a second one - the rename at switch time depends on it.
	newDB := live + "_new"
	if err := provider.CreateDatabaseNamed(ctx, newDB); err != nil {
		t.Fatalf("CreateDatabaseNamed(%s): %v", newDB, err)
	}
	t.Cleanup(func() {
		_ = provider.DropDatabaseNamed(ctx, newDB)
	})

	if got := databaseOwner(ctx, pool, newDB); got != role {
		t.Errorf("owner of %s = %q, want %q", newDB, got, role)
	}
	if publicCanConnect(t, ctx, pool, newDB) {
		t.Errorf("PUBLIC can still CONNECT to %s", newDB)
	}

	var roleCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname = $1`, role).Scan(&roleCount); err != nil {
		t.Fatalf("count roles: %v", err)
	}
	if roleCount != 1 {
		t.Errorf("role %s count = %d, want exactly 1 (aux DBs must reuse it)", role, roleCount)
	}
}

func TestDropTenantRoleIsIdempotent(t *testing.T) {
	ctx := context.Background()
	provider := integrationProvider(t)

	// Dropping a role that never existed must be a no-op, not an error -
	// Temporal retries the compensation activity, so a second attempt must
	// succeed.
	if err := provider.DropTenantRole(ctx, uniqueTenantID("it-absent")); err != nil {
		t.Fatalf("first DropTenantRole on missing role: %v", err)
	}
	if err := provider.DropTenantRole(ctx, uniqueTenantID("it-absent2")); err != nil {
		t.Fatalf("DropTenantRole on missing role must be idempotent: %v", err)
	}
}

func TestMigratePromotionKeepsOwnership(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	provider := integrationProvider(t)

	tenantID := uniqueTenantID("it-promo")
	live := "tenant_" + tenantID
	newDB := live + "_new"
	role := live

	if err := provider.CreateDatabase(ctx, tenantID); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}

	// Cleanup must handle BOTH outcomes: the live DB may be the original one
	// (test failed before promotion) OR the promoted _new (rename succeeded).
	// Drop whichever databases exist under either name, then the role — in
	// that order, because DROP ROLE fails while the role still owns a DB.
	t.Cleanup(func() {
		_ = provider.DropDatabaseNamed(ctx, newDB)
		_ = provider.DropDatabase(ctx, tenantID)
		_ = provider.DropTenantRole(ctx, tenantID)
	})

	if err := provider.CreateDatabaseNamed(ctx, newDB); err != nil {
		t.Fatalf("CreateDatabaseNamed(%s): %v", newDB, err)
	}

	// Emulate migrate's SwitchTraffic: drop the live DB, promote _new into the
	// live name. The renamed DB must STILL be owned by the tenant role.
	if err := provider.DropDatabase(ctx, tenantID); err != nil {
		t.Fatalf("DropDatabase: %v", err)
	}
	if err := provider.RenameDatabase(ctx, newDB, live); err != nil {
		t.Fatalf("RenameDatabase: %v", err)
	}

	if got := databaseOwner(ctx, pool, live); got != role {
		t.Errorf("owner after promotion = %q, want %q (ownership must survive migrate)", got, role)
	}
	if publicCanConnect(t, ctx, pool, live) {
		t.Errorf("PUBLIC can CONNECT after promotion; ownership shape lost")
	}
}

// InspectDatabase is the provider half of reconciliation's actual state: it
// must report the world as it is (exists, owner, PUBLIC CONNECT), and must
// cross-check against the same direct queries the other tests use.
func TestInspectDatabaseReportsReality(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	provider := integrationProvider(t)

	tenantID := uniqueTenantID("it-inspect")
	live := "tenant_" + tenantID
	role := live

	if err := provider.CreateDatabase(ctx, tenantID); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	t.Cleanup(func() {
		_ = provider.DropDatabase(ctx, tenantID)
		_ = provider.DropTenantRole(ctx, tenantID)
	})

	state, err := provider.InspectDatabase(ctx, live)
	if err != nil {
		t.Fatalf("InspectDatabase(%s): %v", live, err)
	}
	if !state.Exists {
		t.Errorf("InspectDatabase: %s should exist", live)
	}
	if state.OwnerRole != role {
		t.Errorf("InspectDatabase owner = %q, want %q (cross-check: %q)",
			state.OwnerRole, role, databaseOwner(ctx, pool, live))
	}
	if state.PublicConnect {
		t.Errorf("InspectDatabase: PUBLIC CONNECT reported open (cross-check: %v)",
			publicCanConnect(t, ctx, pool, live))
	}

	// A database that does not exist must be reported as such, with no owner.
	missing := "tenant_" + uniqueTenantID("it-ghost")
	st, err := provider.InspectDatabase(ctx, missing)
	if err != nil {
		t.Fatalf("InspectDatabase(%s): %v", missing, err)
	}
	if st.Exists {
		t.Errorf("InspectDatabase(%s) reported exists=true for a missing DB", missing)
	}
	if st.OwnerRole != "" || st.PublicConnect {
		t.Errorf("InspectDatabase(%s) reported owner/connect for a missing DB: %+v", missing, st)
	}

	exists, err := provider.RoleExists(ctx, role)
	if err != nil {
		t.Fatalf("RoleExists(%s): %v", role, err)
	}
	if !exists {
		t.Errorf("RoleExists(%s) = false, want true", role)
	}
	ghostRole := "tenant_" + uniqueTenantID("it-ghost")
	exists, err = provider.RoleExists(ctx, ghostRole)
	if err != nil {
		t.Fatalf("RoleExists(%s): %v", ghostRole, err)
	}
	if exists {
		t.Errorf("RoleExists(%s) = true, want false", ghostRole)
	}
}

// EnsureDatabaseOwnership must repair a sabotaged database back to the
// isolation shape — this is the reconciliation repair path against reality.
func TestEnsureDatabaseOwnershipRepairsDrift(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	provider := integrationProvider(t)

	tenantID := uniqueTenantID("it-repair")
	live := "tenant_" + tenantID
	role := live

	if err := provider.CreateDatabase(ctx, tenantID); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	t.Cleanup(func() {
		_ = provider.DropDatabase(ctx, tenantID)
		_ = provider.DropTenantRole(ctx, tenantID)
	})

	// Sabotage the isolation boundary exactly like a real drift:
	// 1. steal ownership (ALTER DATABASE OWNER TO) - pg_dump restores or an
	//    operator's manual fix can clobber ownership;
	// 2. re-open CONNECT to PUBLIC - a careless GRANT opens the tenant DB.
	if err := provider.execPostgres(ctx, fmt.Sprintf(`ALTER DATABASE "%s" OWNER TO temporal`, live)); err != nil {
		t.Fatalf("sabotage owner: %v", err)
	}
	if err := provider.execPostgres(ctx, fmt.Sprintf(`GRANT CONNECT ON DATABASE "%s" TO PUBLIC`, live)); err != nil {
		t.Fatalf("sabotage connect: %v", err)
	}

	if got := databaseOwner(ctx, pool, live); got != "temporal" {
		t.Fatalf("precondition: owner = %q, want temporal (drifted)", got)
	}
	if !publicCanConnect(t, ctx, pool, live) {
		t.Fatalf("precondition: PUBLIC CONNECT should be open (drifted)")
	}

	if err := provider.EnsureDatabaseOwnership(ctx, live); err != nil {
		t.Fatalf("EnsureDatabaseOwnership: %v", err)
	}

	if got := databaseOwner(ctx, pool, live); got != role {
		t.Errorf("owner after repair = %q, want %q", got, role)
	}
	if publicCanConnect(t, ctx, pool, live) {
		t.Errorf("PUBLIC CONNECT still open after repair")
	}

	// Running it again must be a no-op (idempotency for Temporal retries).
	if err := provider.EnsureDatabaseOwnership(ctx, live); err != nil {
		t.Errorf("EnsureDatabaseOwnership second run: %v", err)
	}
}
