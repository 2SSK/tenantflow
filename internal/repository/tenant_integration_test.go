//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/2SSK/tenantflow/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

func uniqueTenantID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func TestCreateAndGetTenant(t *testing.T) {
	pool := testPool(t)
	repo := NewPostgresTenantRepository(pool)
	ctx := context.Background()

	id := uniqueTenantID("test-create")
	wf := "provision-" + id
	want := &model.Tenant{
		TenantID:   id,
		Status:     model.TenantStatusProvisioning,
		WorkflowID: &wf,
	}

	if err := repo.CreateTenant(ctx, want); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	got, err := repo.GetTenant(ctx, id)
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}

	if got.TenantID != want.TenantID {
		t.Errorf("TenantID = %q, want %q", got.TenantID, want.TenantID)
	}

	if got.Status != want.Status {
		t.Errorf("Status = %q, want %q", got.Status, want.Status)
	}

	if got.WorkflowID == nil || *got.WorkflowID != wf {
		t.Errorf("WorkflowID = %v, want %v", got.WorkflowID, wf)
	}

	if got.CreatedAt.IsZero() {
		t.Errorf("CreatedAt is zero; expected the database default now()")
	}
}

func TestCreateTenantIsIdempotent(t *testing.T) {
	pool := testPool(t)
	repo := NewPostgresTenantRepository(pool)
	ctx := context.Background()

	id := uniqueTenantID("test-idem")
	first := &model.Tenant{TenantID: id, Status: model.TenantStatusProvisioning}
	if err := repo.CreateTenant(ctx, first); err != nil {
		t.Fatalf("first CreateTenant: %v", err)
	}

	// Simulate a Temporal activity retry: same insert, same key.
	second := &model.Tenant{TenantID: id, Status: model.TenantStatusActive}
	if err := repo.CreateTenant(ctx, second); err != nil {
		t.Fatalf("second CreateTenant: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tenants WHERE tenant_id = $1`, id).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1 (ON CONFLICT DO NOTHING broken?)", count)
	}
}

func TestGetTenantNotFound(t *testing.T) {
	pool := testPool(t)
	repo := NewPostgresTenantRepository(pool)

	_, err := repo.GetTenant(context.Background(), "definitely-not-a-tenant")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestUpdateTenantStatusFrom(t *testing.T) {
	pool := testPool(t)
	repo := NewPostgresTenantRepository(pool)
	ctx := context.Background()

	id := uniqueTenantID("test-cas-happy")
	ten := &model.Tenant{TenantID: id, Status: model.TenantStatusProvisioning}
	if err := repo.CreateTenant(ctx, ten); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// provisioning -> active is the legal happy path (MarkTenantActive).
	if err := repo.UpdateTenantStatusFrom(ctx, id, model.TenantStatusActive,
		model.TenantStatusProvisioning, model.TenantStatusFailed); err != nil {
		t.Fatalf("UpdateTenantStatusFrom: %v", err)
	}

	got, err := repo.GetTenant(ctx, id)
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.Status != model.TenantStatusActive {
		t.Errorf("Status = %q, want %q", got.Status, model.TenantStatusActive)
	}

	if got.UpdatedAt.Before(got.CreatedAt) {
		t.Errorf("UpdatedAt %v is before CreatedAt %v; the UPDATE should maintain updated_at", got.UpdatedAt, got.CreatedAt)
	}
}

func TestUpdateTenantStatusFromRejectsIllegalSource(t *testing.T) {
	pool := testPool(t)
	repo := NewPostgresTenantRepository(pool)
	ctx := context.Background()

	id := uniqueTenantID("test-cas-conflict")
	ten := &model.Tenant{TenantID: id, Status: model.TenantStatusProvisioning}
	if err := repo.CreateTenant(ctx, ten); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// "deleting" may only be entered from active|failed — a provisioning
	// tenant is an illegal source. This is precisely the create/delete race
	// the guard is meant to close, now enforced by the database itself.
	err := repo.UpdateTenantStatusFrom(ctx, id, model.TenantStatusDeleting,
		model.TenantStatusActive, model.TenantStatusFailed)
	if !errors.Is(err, ErrStatusConflict) {
		t.Fatalf("err = %v, want ErrStatusConflict", err)
	}

	// The rejected CAS must not have partially applied: status is untouched.
	got, err := repo.GetTenant(ctx, id)
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.Status != model.TenantStatusProvisioning {
		t.Errorf("Status = %q, want %q (conflict must not write)",
			got.Status, model.TenantStatusProvisioning)
	}
}
