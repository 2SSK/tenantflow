package workflow

import (
	"fmt"
	"testing"

	"github.com/2SSK/tenantflow/internal/activities"
	"github.com/2SSK/tenantflow/internal/model"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

func activeReconcileSpec() model.TenantSpec {
	return model.TenantSpec{
		TenantID:        "acme-rec",
		Status:          model.TenantStatusActive,
		IsolationMode:   model.IsolationModeDedicated,
		RequireDatabase: true,
		RequireBackup:   true,
	}
}

// ReconcileActualState is healthy: database owned by the tenant role, no
// PUBLIC CONNECT, owner role present, one completed backup.
func healthyActualState() model.ReconcileActualState {
	return model.ReconcileActualState{
		TenantID: "acme-rec",
		Database: model.DatabaseActualState{
			Exists:        true,
			OwnerRole:     "tenant_acme-rec",
			PublicConnect: false,
		},
		RoleExists:       true,
		CompletedBackups: 1,
	}
}

// Already-converged fast path: no drift found, no repair, no failure audit.
func TestReconcileWorkflow_ConvergedWhenNoDrift(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	// Real activities registered so the harness can execute anything not
	// mocked; all deps nil because every activity below is mocked anyway.
	// BackupTenantData lives in BackupActivities, so register that type too.
	env.RegisterActivity(activities.NewReconcileActivities(nil, nil, nil, nil, nil))
	env.RegisterActivity(activities.NewBackupActivities(nil, nil, nil))

	env.OnActivity(activities.ResolveTenantSpecActivityName, mock.Anything, "acme-rec").Return(activeReconcileSpec(), nil)
	env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).Return(healthyActualState(), nil)
	env.OnActivity(activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec", []string{}).Return(nil)

	env.ExecuteWorkflow(ReconcileTenantWorkflow, ReconcileInput{TenantID: "acme-rec"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	env.AssertCalled(t, activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec", []string{})
	env.AssertNotCalled(t, activities.RecordReconcileDriftActivityName, mock.Anything, "acme-rec", mock.Anything)
	env.AssertNotCalled(t, activities.EnsureTenantDatabaseActivityName, mock.Anything, "acme-rec")
	env.AssertNotCalled(t, activities.BackupTenantDataActivityName, mock.Anything, "acme-rec")
	env.AssertNotCalled(t, activities.MarkReconcileFailedActivityName, mock.Anything, "acme-rec", mock.Anything)
}

// Repair path: probe #1 finds the only drift, repair runs (database action +
// backup), probe #2 proves convergence, and the run is reported as repaired.
func TestReconcileWorkflow_RepairsDriftAndConverges(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewReconcileActivities(nil, nil, nil, nil, nil))
	// Repair may fall back to BackupTenantData (BackupActivities), register it.
	env.RegisterActivity(activities.NewBackupActivities(nil, nil, nil))

	drifted := model.ReconcileActualState{
		TenantID:         "acme-rec",
		Database:         model.DatabaseActualState{Exists: false},
		RoleExists:       false,
		CompletedBackups: 0,
	}

	env.OnActivity(activities.ResolveTenantSpecActivityName, mock.Anything, "acme-rec").Return(activeReconcileSpec(), nil)
	// Probe is called twice: once before repair (drift), once after (clean).
	env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).Return(drifted, nil).Once()
	env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).Return(healthyActualState(), nil).Once()
	env.OnActivity(activities.RecordReconcileDriftActivityName, mock.Anything, "acme-rec",
		[]string{model.DriftMissingDatabase, model.DriftMissingRole, model.DriftMissingBackup}).Return(nil)
	env.OnActivity(activities.EnsureTenantDatabaseActivityName, mock.Anything, "acme-rec").Return(nil)
	env.OnActivity(activities.BackupTenantDataActivityName, mock.Anything, "acme-rec").Return(&model.Backup{TenantID: "acme-rec"}, nil)
	env.OnActivity(activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec",
		[]string{model.DriftMissingDatabase, model.DriftMissingRole, model.DriftMissingBackup}).Return(nil)

	env.ExecuteWorkflow(ReconcileTenantWorkflow, ReconcileInput{TenantID: "acme-rec"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	env.AssertCalled(t, activities.RecordReconcileDriftActivityName, mock.Anything, "acme-rec", mock.Anything)
	env.AssertCalled(t, activities.EnsureTenantDatabaseActivityName, mock.Anything, "acme-rec")
	env.AssertCalled(t, activities.BackupTenantDataActivityName, mock.Anything, "acme-rec")
	env.AssertCalled(t, activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec", mock.Anything)
	env.AssertCalled(t, activities.BackupTenantDataActivityName, mock.Anything, "acme-rec")
	env.AssertNotCalled(t, activities.MarkReconcileFailedActivityName, mock.Anything, "acme-rec", mock.Anything)
}

// Unconverged: repair claims success but the re-probe still shows drift.
// The run must fail (lands in the DLQ view) and audit the failure — never
// claim convergence.
func TestReconcileWorkflow_UnconvergedAfterRepairFails(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewReconcileActivities(nil, nil, nil, nil, nil))

	stillDrifted := model.ReconcileActualState{
		TenantID: "acme-rec",
		Database: model.DatabaseActualState{
			Exists:        true,
			OwnerRole:     "someone_else", // ownership drift survives repair
			PublicConnect: true,
		},
		RoleExists:       true,
		CompletedBackups: 1,
	}

	env.OnActivity(activities.ResolveTenantSpecActivityName, mock.Anything, "acme-rec").Return(activeReconcileSpec(), nil)
	env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).Return(stillDrifted, nil).Once()
	env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).Return(stillDrifted, nil).Once()
	env.OnActivity(activities.RecordReconcileDriftActivityName, mock.Anything, "acme-rec",
		[]string{model.DriftWrongOwner, model.DriftPublicConnect}).Return(nil)
	env.OnActivity(activities.EnsureTenantDatabaseActivityName, mock.Anything, "acme-rec").Return(nil)
	env.OnActivity(activities.MarkReconcileFailedActivityName, mock.Anything, "acme-rec", mock.Anything).Return(nil)

	env.ExecuteWorkflow(ReconcileTenantWorkflow, ReconcileInput{TenantID: "acme-rec"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("expected workflow error for unconverged run, got nil")
	}

	env.AssertCalled(t, activities.MarkReconcileFailedActivityName, mock.Anything, "acme-rec", mock.Anything)
	// No backup was drifted, so no backup repair; and it never converged.
	env.AssertNotCalled(t, activities.BackupTenantDataActivityName, mock.Anything, "acme-rec")
	env.AssertNotCalled(t, activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec", mock.Anything)
}

// Non-active tenants converge by construction: nothing was ever promised, so
// the run is recorded as skipped and no probe or repair happens.
func TestReconcileWorkflow_SkipsNonActiveTenant(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewReconcileActivities(nil, nil, nil, nil, nil))

	failedSpec := activeReconcileSpec()
	failedSpec.Status = model.TenantStatusFailed

	env.OnActivity(activities.ResolveTenantSpecActivityName, mock.Anything, "acme-rec").Return(failedSpec, nil)
	env.OnActivity(activities.MarkReconcileSkippedActivityName, mock.Anything, "acme-rec", mock.Anything).Return(nil)

	env.ExecuteWorkflow(ReconcileTenantWorkflow, ReconcileInput{TenantID: "acme-rec"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	env.AssertCalled(t, activities.MarkReconcileSkippedActivityName, mock.Anything, "acme-rec", mock.Anything)
	env.AssertNotCalled(t, activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything)
	env.AssertNotCalled(t, activities.EnsureTenantDatabaseActivityName, mock.Anything, "acme-rec")
	env.AssertNotCalled(t, activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec", mock.Anything)
}

// Shared-mode tenants promise no per-tenant infrastructure, so an active
// shared tenant converges trivially without any database probe.
func TestReconcileWorkflow_SharedTenantConvergesWithoutProbe(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	env.RegisterActivity(activities.NewReconcileActivities(nil, nil, nil, nil, nil))

	sharedSpec := model.TenantSpec{
		TenantID:        "acme-rec",
		Status:          model.TenantStatusActive,
		IsolationMode:   model.IsolationModeShared,
		RequireDatabase: false,
		RequireBackup:   false,
	}

	env.OnActivity(activities.ResolveTenantSpecActivityName, mock.Anything, "acme-rec").Return(sharedSpec, nil)
	env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).Return(model.ReconcileActualState{TenantID: "acme-rec"}, nil)
	env.OnActivity(activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec", []string{}).Return(nil)

	env.ExecuteWorkflow(ReconcileTenantWorkflow, ReconcileInput{TenantID: "acme-rec"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}

	env.AssertCalled(t, activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec", []string{})
	env.AssertNotCalled(t, activities.RecordReconcileDriftActivityName, mock.Anything, "acme-rec", mock.Anything)
	env.AssertNotCalled(t, activities.EnsureTenantDatabaseActivityName, mock.Anything, "acme-rec")
}

// TestReconcileRepairPathActivityFailures is the fail-every-activity loop for
// the BRANCHY repair path — the linear harness in failmatrix_test.go cannot
// express it, because the re-probe must return CLEAN only after the repair
// steps ran (and the harness only supports one stable mock per activity).
//
//	Resolve(0) → Probe(1) → RecordDrift(2) → EnsureDatabase(3) → Backup(4)
//	  → Probe(5, now clean) → MarkConverged(6)
//
// For every failure position the run must: fail; NEVER audit
// MarkReconcileFailed (reserved for the detected-unconverged branch, covered
// by TestReconcileWorkflow_UnconvergedAfterRepairFails); never reach
// MarkConverged unless position 6 itself failed; and the repair composition
// must be position-exact (RecordDrift only from 2, EnsureDatabase only from
// 3, Backup only from 4). Position 5 (the re-probe) gets its own row because
// it shares the Probe activity name with position 1.
func TestReconcileRepairPathActivityFailures(t *testing.T) {
	drifted := model.ReconcileActualState{
		TenantID:         "acme-rec",
		Database:         model.DatabaseActualState{Exists: false},
		RoleExists:       false,
		CompletedBackups: 0,
	}
	healthy := healthyActualState()
	spec := activeReconcileSpec()

	cases := []struct {
		name          string
		fail          string // activity name, or "REPROBE" for position 5
		wantDriftLog  bool   // RecordReconcileDrift ran before the failure
		wantEnsure    bool   // EnsureTenantDatabase ran before the failure
		wantBackup    bool   // BackupTenantData ran before the failure
		wantConverged bool   // MarkConverged called (only when IT failed)
	}{
		{"resolve-fails", activities.ResolveTenantSpecActivityName, false, false, false, false},
		{"probe-fails", activities.ProbeTenantActualStateActivityName, false, false, false, false},
		{"drift-log-fails", activities.RecordReconcileDriftActivityName, true, false, false, false},
		{"ensure-fails", activities.EnsureTenantDatabaseActivityName, true, true, false, false},
		{"backup-fails", activities.BackupTenantDataActivityName, true, true, true, false},
		{"converged-fails", activities.MarkReconcileConvergedActivityName, true, true, true, true},
		{"reprobe-fails", "REPROBE", true, true, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := &testsuite.WorkflowTestSuite{}
			env := ts.NewTestWorkflowEnvironment()

			env.RegisterActivity(activities.NewReconcileActivities(nil, nil, nil, nil, nil))
			env.RegisterActivity(activities.NewBackupActivities(nil, nil, nil))

			mb := func(name string, fail bool, ok []any, failZeros []any, args ...any) {
				c := env.OnActivity(name, append([]any{mock.Anything}, args...)...)
				if fail {
					ret := append([]any{}, failZeros...)
					ret = append(ret, fmt.Errorf("matrix failure of %s", name))
					c.Return(ret...)
					return
				}
				c.Return(ok...)
			}

			mb(activities.ResolveTenantSpecActivityName, tc.fail == activities.ResolveTenantSpecActivityName,
				[]any{spec, nil}, []any{model.TenantSpec{}}, "acme-rec")

			// Probe #1 returns drifted; probe #2 (after repair) returns
			// healthy. Retries matter: the workflow retries failed activities
			// (MaximumAttempts 3), so a probe that must FAIL has to fail on
			// every retry attempt — a .Once() error would let attempt 2
			// succeed and the workflow would continue as if nothing happened.
			switch {
			case tc.fail == activities.ProbeTenantActualStateActivityName:
				// Probe #1 is the failure target: fail persistently.
				env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).
					Return(model.ReconcileActualState{}, fmt.Errorf("matrix failure of probe"))
			case tc.fail == "REPROBE":
				// Probe #1 succeeds (drifted); the re-probe fails persistently.
				env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).
					Return(drifted, nil).Once()
				env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).
					Return(model.ReconcileActualState{}, fmt.Errorf("matrix failure of probe"))
			default:
				env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).
					Return(drifted, nil).Once()
				env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).
					Return(healthy, nil).Once()
			}

			mb(activities.RecordReconcileDriftActivityName, tc.fail == activities.RecordReconcileDriftActivityName,
				[]any{nil}, nil, "acme-rec", mock.Anything)
			mb(activities.EnsureTenantDatabaseActivityName, tc.fail == activities.EnsureTenantDatabaseActivityName,
				[]any{nil}, nil, "acme-rec")
			mb(activities.BackupTenantDataActivityName, tc.fail == activities.BackupTenantDataActivityName,
				[]any{&model.Backup{TenantID: "acme-rec"}, nil}, []any{nil}, "acme-rec")
			mb(activities.MarkReconcileConvergedActivityName, tc.fail == activities.MarkReconcileConvergedActivityName,
				[]any{nil}, nil, "acme-rec", mock.Anything)
			mb(activities.MarkReconcileFailedActivityName, false,
				[]any{nil}, nil, "acme-rec", mock.Anything)

			env.ExecuteWorkflow(ReconcileTenantWorkflow, ReconcileInput{TenantID: "acme-rec"})

			if !env.IsWorkflowCompleted() {
				t.Fatal("workflow did not complete")
			}
			if err := env.GetWorkflowError(); err == nil {
				t.Fatal("expected workflow error, got nil")
			}

			// A mid-repair crash must never audit failure — that audit is the
			// unconverged branch's job, and it must not be polluted by crashes.
			env.AssertNotCalled(t, activities.MarkReconcileFailedActivityName, mock.Anything, "acme-rec", mock.Anything)

			// Composition exactness by failure position.
			assertCall(t, env, tc.wantDriftLog, activities.RecordReconcileDriftActivityName, mock.Anything, "acme-rec", mock.Anything)
			assertCall(t, env, tc.wantEnsure, activities.EnsureTenantDatabaseActivityName, mock.Anything, "acme-rec")
			assertCall(t, env, tc.wantBackup, activities.BackupTenantDataActivityName, mock.Anything, "acme-rec")
			assertCall(t, env, tc.wantConverged, activities.MarkReconcileConvergedActivityName, mock.Anything, "acme-rec", mock.Anything)
		})
	}
}

// assertCall asserts an activity was called (want=true) or not (want=false).
func assertCall(t *testing.T, env *testsuite.TestWorkflowEnvironment, want bool, name string, args ...any) {
	t.Helper()
	if want {
		env.AssertCalled(t, name, args...)
	} else {
		env.AssertNotCalled(t, name, args...)
	}
}
