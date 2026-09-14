//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
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

// TestUpdateTenantStatusFromConcurrentRace proves the CAS is race-safe: many
// concurrent transitions from the same source status, exactly one wins, all
// losers get ErrStatusConflict, and the row ends in the winner's state. This
// is the shared mechanism behind every named concurrency hazard in Phase 13.4
// (DELETE×UPGRADE, DELETE×BACKUP, MIGRATE×BACKUP, RECONCILE×MIGRATE,
// RECONCILE×DELETE): all of them reduce to "two CAS writers collide, one
// commits atomically".
func TestUpdateTenantStatusFromConcurrentRace(t *testing.T) {
	pool := testPool(t)
	repo := NewPostgresTenantRepository(pool)
	ctx := context.Background()

	id := uniqueTenantID("test-cas-race")
	ten := &model.Tenant{TenantID: id, Status: model.TenantStatusProvisioning}
	if err := repo.CreateTenant(ctx, ten); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// 3 racers, each targeting a different successor of "provisioning".
	targets := []model.TenantStatus{
		model.TenantStatusActive,
		model.TenantStatusDeleting,
		model.TenantStatusFailed,
	}
	source := model.TenantStatusProvisioning

	const workers = 30
	type result struct {
		target model.TenantStatus
		err    error
	}
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		target := targets[i%len(targets)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := repo.UpdateTenantStatusFrom(ctx, id, target, source)
			results <- result{target: target, err: err}
		}()
	}
	wg.Wait()
	close(results)

	wins := 0
	conflicts := 0
	winner := model.TenantStatus("")
	var otherErrs []error
	for r := range results {
		switch {
		case r.err == nil:
			wins++
			winner = r.target
		case errors.Is(r.err, ErrStatusConflict):
			conflicts++
		default:
			otherErrs = append(otherErrs, r.err)
		}
	}

	if wins != 1 {
		t.Fatalf("wins = %d, want exactly 1 (the CAS must be atomic: 30 racers, 1 winner)", wins)
	}
	if conflicts != workers-1 {
		t.Errorf("conflicts = %d, want %d; other errors = %v", conflicts, workers-1, otherErrs)
	}

	// The final row must be the winner's target — no interleaving corruption.
	got, err := repo.GetTenant(ctx, id)
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.Status != winner {
		t.Errorf("final Status = %q, want %q (losers must not have written)", got.Status, winner)
	}
	if winner == "" || winner == model.TenantStatusProvisioning {
		t.Errorf("winner = %q, want one of the targets", winner)
	}
}

// TestUpdateTenantStatusFromCommissionedToDeletingRace models the DELETE×*
// family specifically: a delete CAS (active|failed -> deleting) racing a
// second delete CAS from the same source. The tenant is commissioned in a
// background write that must NOT be visible to the delete writers until they
// have already committed — this catches the "both thought they were active"
// blind-spot that a status-only CAS cannot detect without the source check.
func TestUpdateTenantStatusFromSecondDeleteLoses(t *testing.T) {
	pool := testPool(t)
	repo := NewPostgresTenantRepository(pool)
	ctx := context.Background()

	id := uniqueTenantID("test-cas-delrace")
	ten := &model.Tenant{TenantID: id, Status: model.TenantStatusActive}
	if err := repo.CreateTenant(ctx, ten); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Two deletes from the same source: the postgres row-level lock serializes
	// them, so exactly one transition commits.
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- repo.UpdateTenantStatusFrom(ctx, id, model.TenantStatusDeleting,
				model.TenantStatusActive, model.TenantStatusFailed)
		}()
	}
	wg.Wait()
	close(results)

	wins, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrStatusConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d, want 1/1 (double-delete must be one-winner)",
			wins, conflicts)
	}
	got, err := repo.GetTenant(ctx, id)
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.Status != model.TenantStatusDeleting {
		t.Errorf("final Status = %q, want %q", got.Status, model.TenantStatusDeleting)
	}
}
