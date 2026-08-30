package repository

import (
	"context"
	"time"

	"github.com/2SSK/tenantflow/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WorkflowInstanceRepository persists control-plane mirrors of workflow runs.
type WorkflowInstanceRepository interface {
	// InsertRunning records that a workflow run has started executing.
	// Idempotent per (workflow_id, run_id) — safe across activity retries,
	// worker restarts, and duplicate scheduling.
	InsertRunning(ctx context.Context, in model.WorkflowInstance) error
	// MarkFailed records a terminal failed state with the error message.
	MarkFailed(ctx context.Context, workflowID, runID, errMsg string) error
	// ListFailed returns failed runs, most recent first, up to limit.
	ListFailed(ctx context.Context, limit int) ([]model.WorkflowInstance, error)
}

// PostgresWorkflowInstanceRepository implements WorkflowInstanceRepository.
type PostgresWorkflowInstanceRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresWorkflowInstanceRepository builds a repository on a pool.
func NewPostgresWorkflowInstanceRepository(pool *pgxpool.Pool) *PostgresWorkflowInstanceRepository {
	return &PostgresWorkflowInstanceRepository{pool: pool}
}

func (r *PostgresWorkflowInstanceRepository) InsertRunning(ctx context.Context, in model.WorkflowInstance) error {
	_, err := r.pool.Exec(ctx, `
	INSERT INTO workflow_instances (tenant_id, workflow_type, workflow_id, run_id, status, started_at)
	VALUES ($1, $2, $3, $4, 'running', now())
	ON CONFLICT (workflow_id, run_id) DO NOTHING`,
		nullIfEmpty(in.TenantID), in.WorkflowType, in.WorkflowID, in.RunID)
	return err
}

func (r *PostgresWorkflowInstanceRepository) MarkFailed(ctx context.Context, workflowID, runID, errMsg string) error {
	_, err := r.pool.Exec(ctx, `
	UPDATE workflow_instances
	SET status = 'failed', error = jsonb_build_object('message', $1::text), finished_at = now()
	WHERE workflow_id = $2 AND run_id = $3`,
		errMsg, workflowID, runID)
	return err
}

func (r *PostgresWorkflowInstanceRepository) ListFailed(ctx context.Context, limit int) ([]model.WorkflowInstance, error) {
	rows, err := r.pool.Query(ctx, `
	SELECT id, tenant_id, workflow_type, workflow_id, run_id, status,
	       error ->> 'message' AS error_message, started_at, finished_at
	FROM workflow_instances
	WHERE status = 'failed'
	ORDER BY started_at DESC
	LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.WorkflowInstance
	for rows.Next() {
		var inst model.WorkflowInstance
		var tenantID *string
		var finishedAt *time.Time
		if err := rows.Scan(&inst.ID, &tenantID, &inst.WorkflowType, &inst.WorkflowID,
			&inst.RunID, &inst.Status, &inst.Error, &inst.StartedAt, &finishedAt); err != nil {
			return nil, err
		}
		if tenantID != nil {
			inst.TenantID = *tenantID
		}
		inst.FinishedAt = finishedAt
		out = append(out, inst)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// nullIfEmpty converts an empty string into a SQL NULL (used for the nullable
// tenant_id column), avoiding a spurious FK reference to a non-existent tenant.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
