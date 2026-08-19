package worker

import (
	"fmt"
	"log/slog"

	"github.com/2SSK/tenantflow/internal/activities"
	"github.com/2SSK/tenantflow/internal/cloud"
	"github.com/2SSK/tenantflow/internal/repository"
	"github.com/2SSK/tenantflow/internal/temporal"
	tfworkflow "github.com/2SSK/tenantflow/internal/workflow"

	"go.temporal.io/sdk/activity"
	sdkworker "go.temporal.io/sdk/worker"
)

type Worker struct {
	log *slog.Logger
	sdk sdkworker.Worker
}

type activityRegistration struct {
	fn   any
	name string
}

func New(tc *temporal.Client, repo *repository.PostgresTenantRepository, auditRepo repository.AuditRepository, provider cloud.CloudProvider, log *slog.Logger) *Worker {
	provision := activities.NewProvisionActivities(repo, auditRepo, provider)
	deprovision := activities.NewDeprovisionActivities(repo, auditRepo)

	sdk := sdkworker.New(tc.Client, tfworkflow.TaskQueue, sdkworker.Options{})

	registerWorkflows(sdk, []any{
		tfworkflow.ProvisionTenantWorkflow,
		tfworkflow.DeprovisionTenantWorkflow,
	})

	registerActivities(sdk, []activityRegistration{
		{fn: provision.CreateTenantRecord, name: activities.CreateTenantRecordActivityName},
		{fn: provision.ProvisionTenant, name: activities.ProvisionTenantActivityName},
		{fn: provision.MarkTenantActive, name: activities.MarkTenantActiveActivityName},
		{fn: provision.MarkTenantFailed, name: activities.MarkTenantFailedActivityName},
		{fn: provision.DropTenantDatabase, name: activities.DropTenantDatabaseActivityName},
		{fn: deprovision.MarkTenantDeleting, name: activities.MarkTenantDeletingActivityName},
		{fn: deprovision.DeprovisionTenant, name: activities.DeprovisionTenantActivityName},
		{fn: deprovision.MarkTenantDeleted, name: activities.MarkTenantDeletedActivityName},
	})

	return &Worker{log: log, sdk: sdk}
}

func registerWorkflows(sdk sdkworker.Worker, workflows []any) {
	for _, wf := range workflows {
		sdk.RegisterWorkflow(wf)
	}
}

func registerActivities(sdk sdkworker.Worker, registrations []activityRegistration) {
	for _, r := range registrations {
		sdk.RegisterActivityWithOptions(r.fn, activity.RegisterOptions{Name: r.name})
	}
}

func (w *Worker) Run() error {
	w.log.Info("worker polling", "taskQueue", tfworkflow.TaskQueue)
	if err := w.sdk.Run(sdkworker.InterruptCh()); err != nil {
		return fmt.Errorf("worker run: %w", err)
	}
	w.log.Info("worker stopped")
	return nil
}
