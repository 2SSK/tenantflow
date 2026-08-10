package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/2SSK/tenantflow/internal/config"
	"github.com/2SSK/tenantflow/internal/logger"
	"github.com/2SSK/tenantflow/internal/temporal"
	tfworkflow "github.com/2SSK/tenantflow/internal/workflow"
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
	log.Info("temporal created", "address", cfg.TemporalAddress, "namespace", cfg.TemporalNamespace)

	// A worker = one process polling one task queue.
	w := worker.New(tc.Client, tfworkflow.TaskQueue, worker.Options{})
	w.RegisterWorkflow(tfworkflow.ProvisionTenantWorkflow)
	w.RegisterActivity(tfworkflow.ProvisionActivity)

	log.Info("worker polling", "taskQueue", tfworkflow.TaskQueue)
	if err := w.Run(worker.InterruptCh()); err != nil {
		return fmt.Errorf("worker run: %w", err)
	}

	log.Info("worker stopped")
	return nil
}
