package repository

import (
	"context"
	"errors"
	"time"

	"github.com/2SSK/tenantflow/internal/model"
	"github.com/jackc/pgx/v5"
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
	// FindLatestFailed returns the most recent failed run for a tenant and
	// workflow type — the record the DLQ replay needs to decide HOW to resume
	// (a provision replay vs a delete resume). Returns ErrNotFound when the
	// tenant has no such failed run.
	FindLatestFailed(ctx context.Context, tenantID, workflowType string) (model.WorkflowInstance, error)
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

func (r *PostgresWorkflowInstanceRepository) FindLatestFailed(ctx context.Context, tenantID, workflowType string) (model.WorkflowInstance, error) {
	var inst model.WorkflowInstance
	var tenantIDScan *string
	var finishedAt *time.Time
	err := r.pool.QueryRow(ctx, `
	SELECT id, tenant_id, workflow_type, workflow_id, run_id, status,
	       error ->> 'message' AS error_message, started_at, finished_at
	FROM workflow_instances
	WHERE status = 'failed' AND tenant_id = $1 AND workflow_type = $2
	ORDER BY started_at DESC
	LIMIT 1`, tenantID, workflowType).Scan(&inst.ID, &tenantIDScan, &inst.WorkflowType, &inst.WorkflowID,
		&inst.RunID, &inst.Status, &inst.Error, &inst.StartedAt, &finishedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.WorkflowInstance{}, ErrNotFound
		}
		return model.WorkflowInstance{}, err
	}
	if tenantIDScan != nil {
		inst.TenantID = *tenantIDScan
	}
	inst.FinishedAt = finishedAt
	return inst, nil
}

// nullIfEmpty converts an empty string into a SQL NULL (used for the nullable
// tenant_id column), avoiding a spurious FK reference to a non-existent tenant.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
