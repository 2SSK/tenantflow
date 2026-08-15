package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/joho/godotenv"

	"github.com/2SSK/tenantflow/internal/activities"
	"github.com/2SSK/tenantflow/internal/config"
	"github.com/2SSK/tenantflow/internal/database"
	"github.com/2SSK/tenantflow/internal/logger"
	"github.com/2SSK/tenantflow/internal/repository"
	"github.com/2SSK/tenantflow/internal/temporal"
	tfworkflow "github.com/2SSK/tenantflow/internal/workflow"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		slog.Warn("no .env file found, relying on environment", "error", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := logger.New(cfg.Env, os.Stdout)
	slog.SetDefault(log)
	log.Info("tenantflow worker starting", "env", cfg.Env, "taskQueue", tfworkflow.TaskQueue)

	tc, err := temporal.New(ctx, cfg, log)
	if err != nil {
		return fmt.Errorf("temporal: %w", err)
	}
	defer tc.Close()

	db, err := database.New(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close()

	// Dependency chain: pool -> repository -> activities
	repo := repository.NewPostgresTenantRepository(db.Pool)
	acts := activities.NewProvisionActivities(repo)
	depAct := activities.NewDeprovisionActivities(repo)

	w := worker.New(tc.Client, tfworkflow.TaskQueue, worker.Options{})
	w.RegisterWorkflow(tfworkflow.ProvisionTenantWorkflow)
	w.RegisterWorkflow(tfworkflow.DeprovisionTenantWorkflow)

	w.RegisterActivityWithOptions(acts.CreateTenantRecord, activity.RegisterOptions{
		Name: activities.CreateTenantRecordActivityName,
	})
	w.RegisterActivityWithOptions(acts.ProvisionTenant, activity.RegisterOptions{
		Name: activities.ProvisionTenantActivityName,
	})
	w.RegisterActivityWithOptions(acts.MarkTenantActive, activity.RegisterOptions{
		Name: activities.MarkTenantActiveActivityName,
	})
	w.RegisterActivityWithOptions(depAct.MarkTenantDeleting, activity.RegisterOptions{
		Name: activities.MarkTenantDeletingActivityName,
	})
	w.RegisterActivityWithOptions(depAct.DeprovisionTenant, activity.RegisterOptions{
		Name: activities.DeprovisionTenantActivityName,
	})
	w.RegisterActivityWithOptions(depAct.MarkTenantDeleted, activity.RegisterOptions{
		Name: activities.MarkTenantDeletedActivityName,
	})

	log.Info("worker polling", "taskQueue", tfworkflow.TaskQueue)
	if err := w.Run(worker.InterruptCh()); err != nil {
		return fmt.Errorf("worker run: %w", err)
	}

	log.Info("worker stopped")
	return nil
}
