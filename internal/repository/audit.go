package repository

import (
	"context"

	"github.com/2SSK/tenantflow/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AuditRepository interface {
	WriteEvent(ctx context.Context, event *model.AuditEvent) error
	ListEvents(ctx context.Context, tenantID string) ([]model.AuditEvent, error)
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

func (r *PostgresAuditRepository) ListEvents(ctx context.Context, tenantID string) ([]model.AuditEvent, error) {
	rows, err := r.pool.Query(ctx, `
	SELECT id, tenant_id, workflow_id, event_type, actor, payload, created_at
	FROM audit_events
	WHERE tenant_id = $1
	ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []model.AuditEvent
	for rows.Next() {
		var e model.AuditEvent
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.WorkflowID, &e.EventType, &e.Actor, &e.Payload, &e.CreatedAt,
		); err != nil {
			return nil, err
		}

		events = append(events, e)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return events, nil
}
