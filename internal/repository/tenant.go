package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/2SSK/tenantflow/internal/cost"
	"github.com/2SSK/tenantflow/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("tenant not found")
	// ErrStatusConflict reports a compare-and-swap that found the tenant in a
	// state other than the legal source states: a concurrent lifecycle
	// workflow moved it first. The caller must NOT blindly overwrite the
	// status — it lost the race.
	ErrStatusConflict = errors.New("tenant status conflict")
)

type TenantRepository interface {
	CreateTenant(ctx context.Context, t *model.Tenant) error
	GetTenant(ctx context.Context, tenantID string) (*model.Tenant, error)
	ListTenants(ctx context.Context) ([]model.Tenant, error)
	// UpdateTenantStatusFrom atomically transitions the tenant's status. The
	// UPDATE only takes effect when the tenant's current status is one of
	// allowedFrom; otherwise it returns ErrStatusConflict and the status is
	// left untouched. This makes the tenant status a real state machine: every
	// transition is guarded, so concurrent workflows (e.g. provision racing
	// delete) can never clobber each other's writes.
	UpdateTenantStatusFrom(ctx context.Context, tenantID string, status model.TenantStatus, allowedFrom ...model.TenantStatus) error
	// TenantResources returns the measured metadata the cost estimator needs:
	// real database size from pg_database, the isolation tier, workflow run
	// counts (last 30 days) and retained backup count.
	TenantResources(ctx context.Context, tenantID string) (cost.Resources, error)
}

type PostgresTenantRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresTenantRepository builds a repository on top of a connection pool.
func NewPostgresTenantRepository(pool *pgxpool.Pool) *PostgresTenantRepository {
	return &PostgresTenantRepository{pool: pool}
}

func (r *PostgresTenantRepository) CreateTenant(ctx context.Context, t *model.Tenant) error {
	_, err := r.pool.Exec(ctx, `
	INSERT INTO tenants (tenant_id, status, workflow_id, isolation_mode)
	VALUES ($1, $2, $3, $4)
	ON CONFLICT (tenant_id) DO NOTHING`,
		t.TenantID, t.Status, t.WorkflowID, t.IsolationMode)
	return err
}

func (r *PostgresTenantRepository) GetTenant(ctx context.Context, tenantID string) (*model.Tenant, error) {
	t := &model.Tenant{}
	err := r.pool.QueryRow(ctx, `
	SELECT tenant_id, status, workflow_id, isolation_mode, created_at, updated_at
	FROM tenants
	WHERE tenant_id = $1`,
		tenantID).Scan(&t.TenantID, &t.Status, &t.WorkflowID, &t.IsolationMode, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return t, nil
}

// TenantResources measures one tenant's resource metadata. The database
// lookup is exception-safe: pg_database_size would raise an error for a
// missing database, so the CASE guards it. Counts come from the workflow
// ledger and the backups table; the ledger counts every execution started in
// the last 30 days (completed runs are mirrored as "running" — the recorder
// only flips failed ones), which is the honest available proxy for usage.
func (r *PostgresTenantRepository) TenantResources(ctx context.Context, tenantID string) (cost.Resources, error) {
	var (
		res           cost.Resources
		workflowCount int64
		backupCount   int64
	)
	err := r.pool.QueryRow(ctx, `
	SELECT
		CASE WHEN EXISTS (SELECT 1 FROM pg_database WHERE datname = 'tenant_' || $1)
			THEN pg_database_size('tenant_' || $1)
			ELSE 0 END,
		t.isolation_mode,
		(SELECT count(*) FROM workflow_instances w WHERE w.tenant_id = t.tenant_id
			AND w.started_at > now() - interval '30 days'),
		(SELECT count(*) FROM backups b WHERE b.tenant_id = t.tenant_id
			AND b.status = 'completed')
	FROM tenants t
	WHERE t.tenant_id = $1`,
		tenantID).Scan(&res.StorageBytes, &res.IsolationMode, &workflowCount, &backupCount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return cost.Resources{}, ErrNotFound
		}
		return cost.Resources{}, fmt.Errorf("measure resources for tenant %s: %w", tenantID, err)
	}
	res.WorkflowExecutions = int(workflowCount)
	res.BackupCount = int(backupCount)
	return res, nil
}

// ListTenantResources measures every tenant in one query, for the per-scrape
// Prometheus cost collector. A missing tenant database counts as 0 bytes —
// the whole scrape must not fail because one tenant's database is gone.
func (r *PostgresTenantRepository) ListTenantResources(ctx context.Context) ([]cost.TenantResources, error) {
	rows, err := r.pool.Query(ctx, `
	SELECT t.tenant_id, t.isolation_mode,
		CASE WHEN EXISTS (SELECT 1 FROM pg_database WHERE datname = 'tenant_' || t.tenant_id)
			THEN pg_database_size('tenant_' || t.tenant_id) ELSE 0 END,
		(SELECT count(*) FROM workflow_instances w WHERE w.tenant_id = t.tenant_id
			AND w.started_at > now() - interval '30 days'),
		(SELECT count(*) FROM backups b WHERE b.tenant_id = t.tenant_id AND b.status = 'completed')
	FROM tenants t
	WHERE t.status <> 'deleted'`)
	if err != nil {
		return nil, fmt.Errorf("measure all tenant resources: %w", err)
	}
	defer rows.Close()

	out := []cost.TenantResources{}
	for rows.Next() {
		var (
			tr            cost.TenantResources
			workflowCount int64
			backupCount   int64
		)
		if err := rows.Scan(&tr.TenantID, &tr.Resources.IsolationMode, &tr.Resources.StorageBytes, &workflowCount, &backupCount); err != nil {
			return nil, fmt.Errorf("measure all tenant resources: %w", err)
		}
		tr.Resources.WorkflowExecutions = int(workflowCount)
		tr.Resources.BackupCount = int(backupCount)
		out = append(out, tr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("measure all tenant resources: %w", err)
	}
	return out, nil
}

// UpdateTenantStatusFrom is the atomic status transition: the UPDATE carries
// the source-state guard in SQL, so the database — not the application — is
// the arbiter of the state machine. 0 rows affected means the tenant's
// current status was not an allowed source, i.e. a concurrent workflow won.
func (r *PostgresTenantRepository) UpdateTenantStatusFrom(ctx context.Context, tenantID string, status model.TenantStatus, allowedFrom ...model.TenantStatus) error {
	from := make([]string, len(allowedFrom))
	for i, s := range allowedFrom {
		from[i] = string(s)
	}
	res, err := r.pool.Exec(ctx, `
	UPDATE tenants
	SET status = $2, updated_at = now()
	WHERE tenant_id = $1 AND status = ANY($3)`,
		tenantID, string(status), from)
	if err != nil {
		return fmt.Errorf("transition tenant %s to %s: %w", tenantID, status, err)
	}
	if res.RowsAffected() == 0 {
		return ErrStatusConflict
	}
	return nil
}

func (r *PostgresTenantRepository) ListTenants(ctx context.Context) ([]model.Tenant, error) {
	rows, err := r.pool.Query(ctx, `
	SELECT tenant_id, status, workflow_id, isolation_mode, created_at, updated_at
	FROM tenants
	ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tenants := make([]model.Tenant, 0)
	for rows.Next() {
		var t model.Tenant
		if err := rows.Scan(&t.TenantID, &t.Status, &t.WorkflowID, &t.IsolationMode, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tenants, nil
}
