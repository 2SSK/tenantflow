package repository

import (
	"context"

	"github.com/2SSK/tenantflow/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AuditRepository interface {
	WriteEvent(ctx context.Context, event *model.AuditEvent) error
}

type PostgresAuditRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresAuditRepository(pool *pgxpool.Pool) *PostgresAuditRepository {
	return &PostgresAuditRepository{pool: pool}
}

func (r *PostgresAuditRepository) WriteEvent(ctx context.Context, event *model.AuditEvent) error {
	_, err := r.pool.Exec(ctx, `
	INSERT INTO audit_events (tenant_id, workflow_id, event_type, actor, payload)
	VALUES ($1, $2, $3, $4, $5)`,
		event.TenantID, event.WorkflowID, event.EventType, event.Actor, event.Payload)

	return err
}
