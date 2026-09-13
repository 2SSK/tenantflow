package activities

import (
	"context"
	"testing"

	"github.com/2SSK/tenantflow/internal/cloud"
)

// stubCloudProvider implements cloud.CloudProvider with every method failing
// loudly except InspectDatabase, which the test controls. BackupTenantData's
// probe path only needs the read primitive, so a panicking dummy proves the
// skip happens BEFORE any snapshot/restore work — and that a missing database
// never reaches the creation/verification machinery.
type stubCloudProvider struct {
	state cloud.DatabaseState
	err   error
}

func (s *stubCloudProvider) InspectDatabase(ctx context.Context, dbName string) (cloud.DatabaseState, error) {
	return s.state, s.err
}

func (s *stubCloudProvider) CreateDatabase(context.Context, string) error {
	panic("unexpected CreateDatabase")
}
func (s *stubCloudProvider) DropDatabase(context.Context, string) error {
	panic("unexpected DropDatabase")
}
func (s *stubCloudProvider) DropTenantRole(context.Context, string) error {
	panic("unexpected DropTenantRole")
}
func (s *stubCloudProvider) CreateDatabaseNamed(context.Context, string) error {
	panic("unexpected CreateDatabaseNamed")
}
func (s *stubCloudProvider) DropDatabaseNamed(context.Context, string) error {
	panic("unexpected DropDatabaseNamed")
}
func (s *stubCloudProvider) SnapshotDatabase(context.Context, string) (string, error) {
	panic("unexpected SnapshotDatabase")
}
func (s *stubCloudProvider) RestoreDatabaseFromBackup(context.Context, string, string) error {
	panic("unexpected RestoreDatabaseFromBackup")
}
func (s *stubCloudProvider) ValidateDatabase(context.Context, string) error {
	panic("unexpected ValidateDatabase")
}
func (s *stubCloudProvider) RenameDatabase(context.Context, string, string) error {
	panic("unexpected RenameDatabase")
}
func (s *stubCloudProvider) RoleExists(context.Context, string) (bool, error) {
	panic("unexpected RoleExists")
}
func (s *stubCloudProvider) EnsureDatabaseOwnership(context.Context, string) error {
	panic("unexpected EnsureDatabaseOwnership")
}
func (s *stubCloudProvider) EnsureRole(context.Context, string) error { panic("unexpected EnsureRole") }

// TestBackupTenantData_SkipsWhenDatabaseAbsent is the DLQ-replay convergence
// case: a tenant whose database DeprovisionTenant already dropped (previous
// attempt of the same saga failed after teardown). pg_dump on the missing DB
// would fail and mail the replay back to the DLQ forever; the probe must make
// the activity treat an absent DB as "nothing left to capture" and succeed.
func TestBackupTenantData_SkipsWhenDatabaseAbsent(t *testing.T) {
	a := NewBackupActivities(nil, nil, &stubCloudProvider{
		state: cloud.DatabaseState{Exists: false},
	})

	rec, err := a.BackupTenantData(context.Background(), "dedicated2-00000")
	if err != nil {
		t.Fatalf("BackupTenantData with absent DB: %v", err)
	}
	if rec == nil || rec.Filename != "(skipped: database absent)" {
		t.Fatalf("got backup record %+v, want the skipped marker", rec)
	}
}

// TestBackupTenantData_ProbeErrorIsAnError: if the provider cannot even say
// whether the DB exists, the activity must fail (the operator needs to know
// the probe itself is broken) rather than guess.
func TestBackupTenantData_ProbeErrorIsAnError(t *testing.T) {
	a := NewBackupActivities(nil, nil, &stubCloudProvider{
		err: context.DeadlineExceeded,
	})

	if _, err := a.BackupTenantData(context.Background(), "dedicated2-00000"); err == nil {
		t.Fatal("BackupTenantData with failing probe: want error, got nil")
	}
}
