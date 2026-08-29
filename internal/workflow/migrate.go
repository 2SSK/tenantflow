package workflow

import (
	"errors"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/2SSK/tenantflow/internal/activities"
)

type MigrateInput struct {
	TenantID string
}

// MigrateTenantWorkflow runs the migration saga: it builds a brand-new
// database for the tenant off to the side (snapshot -> create _new -> restore
// -> validate), then switches traffic by promoting the _new DB into the live
// tenant name. The live tenant stays "active" the whole time.
//
// Compensation strategy (why the `switched` flag exists):
//   - Before the switch, the live DB is untouched and the migration produced a
//     disposable _new DB. If anything fails here we just drop _new and the
//     tenant keeps running as before.
//   - After the switch, _new has been renamed into the live name and the old
//     DB is gone. SwitchTraffic self-heals on an interrupted switch by
//     restoring from backup, so there is no further DB work for the saga here.
func MigrateTenantWorkflow(ctx workflow.Context, in MigrateInput) (err error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("Starting MigrateTenantWorkflow", "TenantID", in.TenantID)

	info := workflow.GetInfo(ctx)
	workflowID := info.WorkflowExecution.ID

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})

	var switched bool

	defer func() {
		if err != nil {
			if !switched {
				// The live DB was never replaced; discard the disposable _new
				// DB (idempotent IF EXISTS) and leave the tenant running.
				dropErr := workflow.ExecuteActivity(actCtx, activities.DropTenantAuxDatabaseActivityName, in.TenantID).Get(actCtx, nil)
				err = errors.Join(err, dropErr)
			}
			failErr := workflow.ExecuteActivity(actCtx, activities.MarkTenantMigrateFailedActivityName, in.TenantID).Get(actCtx, nil)
			err = errors.Join(err, failErr)
		}
	}()

	if err = workflow.ExecuteActivity(actCtx, activities.MarkTenantMigratingActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	var backupName string
	if err = workflow.ExecuteActivity(actCtx, activities.MigrateDataActivityName, in.TenantID).Get(actCtx, &backupName); err != nil {
		return err
	}

	if err = workflow.ExecuteActivity(actCtx, activities.SwitchTrafficActivityName, in.TenantID, backupName).Get(actCtx, nil); err != nil {
		return err
	}
	switched = true

	if err = workflow.ExecuteActivity(actCtx, activities.MarkTenantMigratedActivityName, in.TenantID).Get(actCtx, nil); err != nil {
		return err
	}

	logger.Info("MigrateTenantWorkflow completed successfully", "TenantID", in.TenantID, "WorkflowID", workflowID)
	return nil
}
