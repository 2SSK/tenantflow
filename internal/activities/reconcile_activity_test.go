package activities

import (
	"context"
	"errors"
	"testing"

	"github.com/2SSK/tenantflow/internal/cloud"
	"github.com/2SSK/tenantflow/internal/model"
	"github.com/2SSK/tenantflow/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// stubBackupRepo drives only the legs RestoreTenantFromBackup touches.
type stubBackupRepo struct {
	backups []model.Backup
	err     error
}

func (s *stubBackupRepo) ListBackups(context.Context, string) ([]model.Backup, error) {
	return s.backups, s.err
}

func (*stubBackupRepo) CreateBackup(context.Context, *model.Backup) (*model.Backup, error) {
	return nil, errors.New("unexpected CreateBackup")
}
func (*stubBackupRepo) GetBackup(context.Context, int64) (*model.Backup, error) {
	return nil, errors.New("unexpected GetBackup")
}
func (*stubBackupRepo) MarkBackupCompleted(context.Context, int64) error {
	return errors.New("unexpected MarkBackupCompleted")
}
func (*stubBackupRepo) MarkBackupFailed(context.Context, int64) error {
	return errors.New("unexpected MarkBackupFailed")
}

// stubAuditRepo records the single audit write the restore leg performs.
type stubAuditRepo struct {
	events []*model.AuditEvent
}

func (s *stubAuditRepo) WriteEvent(ctx context.Context, event *model.AuditEvent) error {
	s.events = append(s.events, event)
	return nil
}
func (*stubAuditRepo) ListEvents(context.Context, string) ([]model.AuditEvent, error) {
	return nil, errors.New("unexpected ListEvents")
}

func completedBackup(id int64, filename string) model.Backup {
	return model.Backup{ID: id, TenantID: "acme-rec", Filename: filename, Status: model.BackupStatusCompleted}
}

// runRestore executes the activity inside a real Temporal activity environment
// (RestoreTenantFromBackup audits via activity.GetInfo, which panics outside
// one) and returns the resulting error.
func runRestore(t *testing.T, a *ReconcileActivities, tenantID string) error {
	t.Helper()
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestActivityEnvironment()
	env.RegisterActivity(a.RestoreTenantFromBackup)
	_, err := env.ExecuteActivity(a.RestoreTenantFromBackup, tenantID)
	return err
}

// The happy path: latest completed backup is restored into the recreated
// database, isolation shape is re-applied, and the restore is audited.
func TestRestoreTenantFromBackup_HappyPath(t *testing.T) {
	backupRepo := &stubBackupRepo{backups: []model.Backup{
		{ID: 2, TenantID: "acme-rec", Filename: "old.sql", Status: model.BackupStatusFailed}, // newest, but unverified
		completedBackup(1, "verified.sql"),
	}}
	auditRepo := &stubAuditRepo{}
	provider := &stubCloudProvider{}

	a := NewReconcileActivities(nil, auditRepo, backupRepo, provider, nil)

	err := runRestore(t, a, "acme-rec")
	require.NoError(t, err)

	// ListBackups returns newest-first (created_at DESC from the real repo);
	// the FIRST completed backup wins — here the failed one is skipped.
	require.Len(t, provider.createdDatabases, 1, "database must be created before restore")
	require.Contains(t, provider.restoredTargets, cloud.TenantDatabaseName("acme-rec"))
	require.Contains(t, provider.restoredFilenames, "verified.sql")
	require.Equal(t, 1, provider.ownershipRepairs, "ownership must be re-applied after restore")
	require.Equal(t, 1, provider.validated, "restored database must be validated")
	require.Len(t, auditRepo.events, 1)
	assert.Equal(t, model.AuditEventTenantReconcileRestored, auditRepo.events[0].EventType)
}

// Data-safety sentinel: no completed/verified backup → ErrNoVerifiedBackup,
// so the workflow can escalate instead of creating an empty database.
func TestRestoreTenantFromBackup_NoVerifiedBackupReturnsSentinel(t *testing.T) {
	backupRepo := &stubBackupRepo{backups: []model.Backup{
		{ID: 1, TenantID: "acme-rec", Filename: "broken.sql", Status: model.BackupStatusFailed},
		{ID: 2, TenantID: "acme-rec", Filename: "vanished.sql", Status: model.BackupStatusPending},
	}}
	provider := &stubCloudProvider{}

	a := NewReconcileActivities(nil, nil, backupRepo, provider, nil)

	err := runRestore(t, a, "acme-rec")
	require.Error(t, err)
	// The sentinel arrives as a typed non-retryable ApplicationError so it
	// survives the activity→workflow boundary; IsNoVerifiedBackup is the
	// single discovery point for both forms.
	assert.True(t, IsNoVerifiedBackup(err), "want no-verified-backup escalation, got: %v", err)
	require.Empty(t, provider.createdDatabases, "no database may be created without a verified backup")
	require.Empty(t, provider.restoredTargets)
}

// TestRestoreTenantFromBackup_PreDropsResidue simulates the retry
// window the pre-drop closes: attempt 1 created the database and crashed
// mid-restore, so attempt 2 must NOT naively re-run CreateDatabase (which
// would die with "database already exists"). The tenant's database was
// reported missing (DriftMissingDatabase) — the only trustworthy state is a
// restored, verified backup — so any residue must be dropped BEFORE the
// create, then restore into the freshly created database.
func TestRestoreTenantFromBackup_PreDropsResidue(t *testing.T) {
	backupRepo := &stubBackupRepo{backups: []model.Backup{completedBackup(1, "verified.sql")}}
	auditRepo := &stubAuditRepo{}
	provider := &stubCloudProvider{}

	a := NewReconcileActivities(nil, auditRepo, backupRepo, provider, nil)

	err := runRestore(t, a, "acme-rec")
	require.NoError(t, err)

	// The drop MUST precede the create — this is the whole point of the test.
	require.Equal(t, []string{"drop:acme-rec", "create:acme-rec"}, provider.ops,
		"crash residue must be pre-dropped before the create (and never after)")
	require.Contains(t, provider.createdDatabases, "acme-rec")
	require.Contains(t, provider.restoredTargets, cloud.TenantDatabaseName("acme-rec"))
	require.Contains(t, provider.restoredFilenames, "verified.sql")
	require.Equal(t, 1, provider.ownershipRepairs, "ownership must be re-applied after restore")
	require.Equal(t, 1, provider.validated, "restored database must be validated")
	require.Len(t, auditRepo.events, 1)
	assert.Equal(t, model.AuditEventTenantReconcileRestored, auditRepo.events[0].EventType)
}

// A probe/timeout failure listing backups is a real error, not a sentinel.
func TestRestoreTenantFromBackup_ListErrorPropagates(t *testing.T) {
	backupRepo := &stubBackupRepo{err: errors.New("connection reset")}
	provider := &stubCloudProvider{}

	a := NewReconcileActivities(nil, nil, backupRepo, provider, nil)

	err := runRestore(t, a, "acme-rec")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoVerifiedBackup)
	assert.Contains(t, err.Error(), "connection reset")
	assert.Empty(t, provider.createdDatabases)
}

var _ repository.BackupRepository = (*stubBackupRepo)(nil)
var _ repository.AuditRepository = (*stubAuditRepo)(nil)
