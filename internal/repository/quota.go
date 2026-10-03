package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/2SSK/tenantflow/internal/billing"
)

// PostgresQuotaStore persists a tenant's quota in Postgres instead of process
// memory. The API and the worker are separate binaries, so an in-memory store
// in one process is invisible to the other; writing quotas to the database
// makes them readable by both and durable across restarts.
//
// Get mirrors InMemoryQuotaStore: a tenant with no row yet is on the default
// plan (billing.DefaultQuota) rather than an error.
type PostgresQuotaStore struct {
	pool *pgxpool.Pool
}

func NewPostgresQuotaStore(pool *pgxpool.Pool) *PostgresQuotaStore {
	return &PostgresQuotaStore{pool: pool}
}

func (s *PostgresQuotaStore) Set(ctx context.Context, tenantID string, q billing.Quota) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO tenant_quotas (tenant_id, max_users, max_storage_gb, max_seats)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (tenant_id) DO UPDATE SET
		   max_users      = EXCLUDED.max_users,
		   max_storage_gb = EXCLUDED.max_storage_gb,
		   max_seats      = EXCLUDED.max_seats,
		   updated_at     = NOW()`,
		tenantID, q.MaxUsers, q.MaxStorageGB, q.MaxSeats)
	if err != nil {
		return fmt.Errorf("set quota: %w", err)
	}
	return nil
}

func (s *PostgresQuotaStore) Get(ctx context.Context, tenantID string) (billing.Quota, error) {
	var q billing.Quota
	err := s.pool.QueryRow(ctx,
		`SELECT max_users, max_storage_gb, max_seats
		 FROM tenant_quotas
		 WHERE tenant_id = $1`,
		tenantID).Scan(&q.MaxUsers, &q.MaxStorageGB, &q.MaxSeats)
	if errors.Is(err, pgx.ErrNoRows) {
		return billing.DefaultQuota, nil
	}
	if err != nil {
		return billing.Quota{}, fmt.Errorf("get quota: %w", err)
	}
	return q, nil
}