package cloud

import (
	"strings"
	"testing"
)

func TestTenantOwnerRole(t *testing.T) {
	tests := []struct {
		name    string
		dbName  string
		want    string
		wantErr bool
	}{
		{name: "live database", dbName: "tenant_demo-b", want: "tenant_demo-b"},
		{name: "migrate target", dbName: "tenant_demo-b_new", want: "tenant_demo-b"},
		{name: "backup verification", dbName: "tenant_demo-b_temp", want: "tenant_demo-b"},
		{name: "hyphen id with suffix", dbName: "tenant_rbac-admin-final_new", want: "tenant_rbac-admin-final"},
		{name: "rejects shell metacharacters", dbName: "tenant_x; DROP TABLE tenants", wantErr: true},
		{name: "rejects spaces", dbName: "tenant x", wantErr: true},
		{name: "rejects quotes", dbName: `tenant_x"`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tenantOwnerRole(tt.dbName)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("tenantOwnerRole(%q) = %q, want error", tt.dbName, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("tenantOwnerRole(%q): %v", tt.dbName, err)
			}
			if got != tt.want {
				t.Errorf("tenantOwnerRole(%q) = %q, want %q", tt.dbName, got, tt.want)
			}
		})
	}
}

func TestOwnershipStatements(t *testing.T) {
	stmts, err := ownershipStatements("tenant_demo-b_new")
	if err != nil {
		t.Fatalf("ownershipStatements: %v", err)
	}
	if len(stmts) != 3 {
		t.Fatalf("got %d statements, want 3: %v", len(stmts), stmts)
	}

	// 1. Role creation is guarded by IF NOT EXISTS so migrate/backup retries
	// can re-apply ownership to an already-existing role without failing.
	ensure := stmts[0]
	if !strings.Contains(ensure, "IF NOT EXISTS") {
		t.Errorf("role-ensure statement must be idempotent, got: %s", ensure)
	}
	if !strings.Contains(ensure, "rolname = 'tenant_demo-b'") {
		t.Errorf("role-ensure must look up the unsuffixed role, got: %s", ensure)
	}
	if !strings.Contains(ensure, `CREATE ROLE "tenant_demo-b"`) {
		t.Errorf("role-ensure must create the unsuffixed role, got: %s", ensure)
	}

	// 2. The database itself is owned by the tenant role.
	alter := stmts[1]
	if !strings.Contains(alter, `ALTER DATABASE "tenant_demo-b_new" OWNER TO "tenant_demo-b"`) {
		t.Errorf("owner statement mismatch, got: %s", alter)
	}

	// 3. PUBLIC can no longer connect; only the owner role (and superusers)
	// can reach the tenant's database.
	revoke := stmts[2]
	if !strings.Contains(revoke, `REVOKE CONNECT ON DATABASE "tenant_demo-b_new" FROM PUBLIC`) {
		t.Errorf("revoke statement mismatch, got: %s", revoke)
	}
}

func TestOwnershipStatementsRejectsInvalidDBName(t *testing.T) {
	if _, err := ownershipStatements(`tenant_x"; DROP SCHEMA public; --`); err == nil {
		t.Fatal("ownershipStatements accepted an injection attempt")
	}
}
