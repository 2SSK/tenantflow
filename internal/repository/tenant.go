package repository

import (
	"context"
	"errors"

	"github.com/2SSK/tenantflow/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("tenant not found")

type TenantRepository interface {
	CreateTenant(ctx context.Context, t *model.Tenant) error
	GetTenant(ctx context.Context, tenantID string) (*model.Tenant, error)
	UpdateTenantStatus(ctx context.Context, tenantID string, status model.TenantStatus) error
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
	INSERT INTO tenants (tenant_id, status, workflow_id)
	VALUES ($1, $2, $3)
	ON CONFLICT (tenant_id) DO NOTHING`,
		t.TenantID, t.Status, t.WorkflowID)
	return err
}

func (r *PostgresTenantRepository) GetTenant(ctx context.Context, tenantID string) (*model.Tenant, error) {
	t := &model.Tenant{}
	err := r.pool.QueryRow(ctx, `
	SELECT tenant_id, status, workflow_id, created_at, updated_at
	FROM tenants
	WHERE tenant_id = $1`,
		tenantID).Scan(&t.TenantID, &t.Status, &t.WorkflowID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return t, nil
}

func (r *PostgresTenantRepository) UpdateTenantStatus(ctx context.Context, tenantID string, status model.TenantStatus) error {
	_, err := r.pool.Exec(ctx, `
	UPDATE tenants
	SET status = $2, updated_at = now()
	WHERE tenant_id = $1`,
		tenantID, status)
	return err
}
