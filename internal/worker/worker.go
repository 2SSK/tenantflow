package worker

import (
	"fmt"
	"log/slog"

	"github.com/2SSK/tenantflow/internal/activities"
	"github.com/2SSK/tenantflow/internal/billing"
	"github.com/2SSK/tenantflow/internal/cloud"
	"github.com/2SSK/tenantflow/internal/identity"
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

func New(tc *temporal.Client, repo *repository.PostgresTenantRepository, auditRepo repository.AuditRepository, backupRepo repository.BackupRepository, provider cloud.CloudProvider, identityProvider identity.IdentityProvider, log *slog.Logger) *Worker {
	provision := activities.NewProvisionActivities(repo, auditRepo, provider)
	deprovision := activities.NewDeprovisionActivities(repo, auditRepo)
	identityActs := activities.NewIdentityActivities(identityProvider)
	quotaStore := billing.NewInMemoryQuotaStore()
	upgrade := activities.NewUpgradeActivities(repo, auditRepo, quotaStore)
	migrate := activities.NewMigrateActivities(auditRepo, provider)
	backup := activities.NewBackupActivities(backupRepo, auditRepo, provider)
	restore := activities.NewRestoreActivities(backupRepo, auditRepo, provider)

	sdk := sdkworker.New(tc.Client, tfworkflow.TaskQueue, sdkworker.Options{})

	registerWorkflows(sdk, []any{
		tfworkflow.ProvisionTenantWorkflow,
		tfworkflow.DeprovisionTenantWorkflow,
		tfworkflow.UpgradeTenantWorkflow,
		tfworkflow.MigrateTenantWorkflow,
		tfworkflow.BackupTenantWorkflow,
		tfworkflow.RestoreTenantWorkflow,
	})

	registerActivities(sdk, []activityRegistration{
		{fn: provision.CreateTenantRecord, name: activities.CreateTenantRecordActivityName},
		{fn: provision.ProvisionTenant, name: activities.ProvisionTenantActivityName},
		{fn: provision.MarkTenantActive, name: activities.MarkTenantActiveActivityName},
		{fn: provision.MarkTenantFailed, name: activities.MarkTenantFailedActivityName},
		{fn: provision.DropTenantDatabase, name: activities.DropTenantDatabaseActivityName},
		{fn: identityActs.ProvisionTenantIdentity, name: activities.ProvisionTenantIdentityActivityName},
		{fn: identityActs.DeleteTenantIdentity, name: activities.DeleteTenantIdentityActivityName},
		{fn: deprovision.MarkTenantDeleting, name: activities.MarkTenantDeletingActivityName},
		{fn: deprovision.DeprovisionTenant, name: activities.DeprovisionTenantActivityName},
		{fn: deprovision.MarkTenantDeleted, name: activities.MarkTenantDeletedActivityName},
		{fn: upgrade.MarkTenantUpgrading, name: activities.MarkTenantUpgradingActivityName},
		{fn: upgrade.VerifyTenantActive, name: activities.VerifyTenantActiveActivityName},
		{fn: upgrade.RaiseQuotas, name: activities.RaiseQuotasActivityName},
		{fn: upgrade.EnableFeatures, name: activities.EnableFeaturesActivityName},
		{fn: upgrade.UpdateBilling, name: activities.UpdateBillingActivityName},
		{fn: upgrade.RollbackQuotas, name: activities.RollbackQuotasActivityName},
		{fn: upgrade.MarkTenantUpgraded, name: activities.MarkTenantUpgradedActivityName},
		{fn: upgrade.MarkTenantUpgradeFailed, name: activities.MarkTenantUpgradeFailedActivityName},
		{fn: migrate.MarkTenantMigrating, name: activities.MarkTenantMigratingActivityName},
		{fn: migrate.MigrateData, name: activities.MigrateDataActivityName},
		{fn: migrate.SwitchTraffic, name: activities.SwitchTrafficActivityName},
		{fn: migrate.MarkTenantMigrated, name: activities.MarkTenantMigratedActivityName},
		{fn: migrate.DropTenantAuxDatabase, name: activities.DropTenantAuxDatabaseActivityName},
		{fn: migrate.MarkTenantMigrateFailed, name: activities.MarkTenantMigrateFailedActivityName},
		{fn: backup.MarkTenantBackingUp, name: activities.MarkTenantBackingUpActivityName},
		{fn: backup.BackupTenantData, name: activities.BackupTenantDataActivityName},
		{fn: backup.MarkTenantBackedUp, name: activities.MarkTenantBackedUpActivityName},
		{fn: backup.MarkTenantBackupFailed, name: activities.MarkTenantBackupFailedActivityName},
		{fn: restore.MarkTenantRestoring, name: activities.MarkTenantRestoringActivityName},
		{fn: restore.PreRestoreSnapshot, name: activities.PreRestoreSnapshotActivityName},
		{fn: restore.RestoreData, name: activities.RestoreDataActivityName},
		{fn: restore.MarkTenantRestored, name: activities.MarkTenantRestoredActivityName},
		{fn: restore.RestoreRollback, name: activities.RestoreRollbackActivityName},
		{fn: restore.MarkTenantRestoreFailed, name: activities.MarkTenantRestoreFailedActivityName},
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
