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
