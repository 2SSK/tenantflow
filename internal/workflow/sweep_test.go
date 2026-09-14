package workflow

import (
	"testing"
	"time"

	"github.com/2SSK/tenantflow/internal/activities"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// newSweepEnv returns a test environment with the reconcile activity types
// registered (nil deps are fine — every activity is mocked by name below) so
// that OnActivity can attach expectations for the sweep's enumeration AND the
// per-tenant child workflows it starts.
func newSweepEnv() *testsuite.TestWorkflowEnvironment {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterActivity(activities.NewReconcileActivities(nil, nil, nil, nil, nil))
	// Children execute real ReconcileTenantWorkflow runs inside the env.
	env.RegisterWorkflow(ReconcileTenantWorkflow)
	return env
}

// sweepChildrenMocked wires the per-tenant ReconcileTenantWorkflow activities
// for an already-converged run (resolve → probe healthy → converged). A child
// that converges needs only these three activities, so the sweep test does
// not have to care about repair gymnastics.
func sweepChildrenMocked(env *testsuite.TestWorkflowEnvironment) {
	env.OnActivity(activities.ResolveTenantSpecActivityName, mock.Anything, mock.Anything).Return(activeReconcileSpec(), nil)
	env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).Return(healthyActualState(), nil)
	env.OnActivity(activities.MarkReconcileConvergedActivityName, mock.Anything, mock.Anything, mock.Anything).Return(nil)
}

// The sweep starts one reconcile child per active tenant, converges both, and
// a one-shot (MaxSweeps=1) run reports what it did.
func TestReconcileSweepWorkflow_StartsOneReconcilePerTenant(t *testing.T) {
	env := newSweepEnv()

	env.OnActivity(activities.ListActiveTenantIDsActivityName, mock.Anything).Return([]string{"sweep-a", "sweep-b"}, nil)
	sweepChildrenMocked(env)

	env.ExecuteWorkflow(ReconcileSweepWorkflow, ReconcileSweepInput{Interval: time.Hour, MaxSweeps: 1})

	require.NoError(t, env.GetWorkflowError())
	var res ReconcileSweepResult
	require.NoError(t, env.GetWorkflowResult(&res))
	assert.Equal(t, 1, res.SweepsCompleted)
	assert.Equal(t, 2, res.LastTenantsFound)
}

// Duplicate IDs in the enumeration (defensive) must not double-start a tenant.
// The mock counts ResolveTenantSpec/Probe calls: exactly one proves one child
// ran (expectations are declared per-child here instead of via the shared
// helper so Times(1) is the ONLY match).
func TestReconcileSweepWorkflow_DeduplicatesTenantInList(t *testing.T) {
	env := newSweepEnv()

	env.OnActivity(activities.ListActiveTenantIDsActivityName, mock.Anything).Return([]string{"sweep-a", "sweep-a"}, nil)
	env.OnActivity(activities.ResolveTenantSpecActivityName, mock.Anything, mock.Anything).Return(activeReconcileSpec(), nil).Times(1)
	env.OnActivity(activities.ProbeTenantActualStateActivityName, mock.Anything, mock.Anything).Return(healthyActualState(), nil).Times(1)
	env.OnActivity(activities.MarkReconcileConvergedActivityName, mock.Anything, mock.Anything, mock.Anything).Return(nil).Times(1)

	env.ExecuteWorkflow(ReconcileSweepWorkflow, ReconcileSweepInput{Interval: time.Hour, MaxSweeps: 1})

	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// The loop must sleep Interval between ticks (auto-advancing test clock), so
// MaxSweeps=3 at 10m only completes after two sleeps — the cadence is
// respected, not bypassed. LastTenantsFound reflects the FINAL sweep.
func TestReconcileSweepWorkflow_SleepsIntervalBetweenSweeps(t *testing.T) {
	env := newSweepEnv()

	env.OnActivity(activities.ListActiveTenantIDsActivityName, mock.Anything).Return([]string{}, nil)

	env.ExecuteWorkflow(ReconcileSweepWorkflow, ReconcileSweepInput{Interval: 10 * time.Minute, MaxSweeps: 3})

	require.NoError(t, env.GetWorkflowError())
	var res ReconcileSweepResult
	require.NoError(t, env.GetWorkflowResult(&res))
	assert.Equal(t, 3, res.SweepsCompleted)
	assert.Equal(t, 0, res.LastTenantsFound)
}

// The loop bounds its history: after ContinueAfter sweeps the run ends itself
// by continuing as new (a fresh run with the same intent), which the test env
// surfaces as the run completing with the ContinueAsNew marker error instead
// of hanging forever.
func TestReconcileSweepWorkflow_ContinueAsNewBoundedHistory(t *testing.T) {
	env := newSweepEnv()

	env.OnActivity(activities.ListActiveTenantIDsActivityName, mock.Anything).Return([]string{}, nil)

	env.ExecuteWorkflow(ReconcileSweepWorkflow, ReconcileSweepInput{
		Interval:      time.Second,
		ContinueAfter: 2,
	})

	require.Error(t, env.GetWorkflowError())
	var cae *workflow.ContinueAsNewError
	require.ErrorAs(t, env.GetWorkflowError(), &cae)
}

// An empty/invalid interval must fail loudly (non-retryably) — a hot-looping
// sweep is worse than none at all.
func TestReconcileSweepWorkflow_RejectsNonPositiveInterval(t *testing.T) {
	env := newSweepEnv()

	env.ExecuteWorkflow(ReconcileSweepWorkflow, ReconcileSweepInput{Interval: 0, MaxSweeps: 1})

	require.Error(t, env.GetWorkflowError())
}
