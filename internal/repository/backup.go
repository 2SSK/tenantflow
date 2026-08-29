package repository

import (
	"context"
	"errors"

	"github.com/2SSK/tenantflow/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BackupRepository persists tenant backup records in the control plane.
type BackupRepository interface {
	CreateBackup(ctx context.Context, b *model.Backup) (*model.Backup, error)
	GetBackup(ctx context.Context, id int64) (*model.Backup, error)
	ListBackups(ctx context.Context, tenantID string) ([]model.Backup, error)
	MarkBackupCompleted(ctx context.Context, id int64) error
	MarkBackupFailed(ctx context.Context, id int64) error
}

type PostgresBackupRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresBackupRepository(pool *pgxpool.Pool) *PostgresBackupRepository {
	return &PostgresBackupRepository{pool: pool}
}

// CreateBackup inserts a pending backup row and returns it populated with the
// auto-generated ID and timestamp.
func (r *PostgresBackupRepository) CreateBackup(ctx context.Context, b *model.Backup) (*model.Backup, error) {
	created := &model.Backup{}
	err := r.pool.QueryRow(ctx, `
	INSERT INTO backups (tenant_id, filename, status)
	VALUES ($1, $2, 'pending')
	RETURNING id, tenant_id, filename, status, created_at, completed_at`,
		b.TenantID, b.Filename).Scan(
		&created.ID, &created.TenantID, &created.Filename, &created.Status, &created.CreatedAt, &created.CompletedAt)
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (r *PostgresBackupRepository) GetBackup(ctx context.Context, id int64) (*model.Backup, error) {
	b := &model.Backup{}
	err := r.pool.QueryRow(ctx, `
	SELECT id, tenant_id, filename, status, created_at, completed_at
	FROM backups
	WHERE id = $1`, id).Scan(&b.ID, &b.TenantID, &b.Filename, &b.Status, &b.CreatedAt, &b.CompletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return b, nil
}

func (r *PostgresBackupRepository) ListBackups(ctx context.Context, tenantID string) ([]model.Backup, error) {
	rows, err := r.pool.Query(ctx, `
	SELECT id, tenant_id, filename, status, created_at, completed_at
	FROM backups
	WHERE tenant_id = $1
	ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	backups := make([]model.Backup, 0)
	for rows.Next() {
		var b model.Backup
		if err := rows.Scan(&b.ID, &b.TenantID, &b.Filename, &b.Status, &b.CreatedAt, &b.CompletedAt); err != nil {
			return nil, err
		}
		backups = append(backups, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return backups, nil
}

func (r *PostgresBackupRepository) MarkBackupCompleted(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx, `
	UPDATE backups
	SET status = 'completed', completed_at = now()
	WHERE id = $1`, id)
	return err
}

func (r *PostgresBackupRepository) MarkBackupFailed(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx, `
	UPDATE backups
	SET status = 'failed'
	WHERE id = $1`, id)
	return err
}
