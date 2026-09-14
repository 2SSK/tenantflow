package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/2SSK/tenantflow/internal/app"
	"github.com/2SSK/tenantflow/internal/chaos"
	"github.com/2SSK/tenantflow/internal/metrics"
	tfworker "github.com/2SSK/tenantflow/internal/worker"
	tfworkflow "github.com/2SSK/tenantflow/internal/workflow"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	reg := metrics.New()
	a, err := app.New(ctx, "tenantflow worker", metrics.NewTemporalHandler(reg))
	if err != nil {
		return err
	}
	defer a.Close()

	w := tfworker.New(a.TC, a.Repo, a.AuditRepo, a.BackupRepo, a.Provider, a.Identity,
		a.InstanceRepo,
		chaos.NewController(a.Config.Chaos.Rate, a.Config.Chaos.Activities), reg, a.Log)

	// The worker has no other HTTP surface; a tiny server exposes its
	// registry (custom + temporal_* SDK metrics) for Prometheus scraping.
	metricsSrv := &http.Server{
		Addr:              a.Config.WorkerMetricsAddr,
		Handler:           reg.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	metricsErr := make(chan error, 1)
	go func() {
		a.Log.Info("metrics listening", "addr", metricsSrv.Addr)
		metricsErr <- metricsSrv.ListenAndServe()
	}()

	if a.Config.ReconcileSweepInterval > 0 {
		if err := startReconcileSweep(ctx, a); err != nil {
			return err
		}
	} else {
		a.Log.Info("reconcile sweep disabled", "interval", a.Config.ReconcileSweepInterval)
	}

	runErr := w.Run()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("metrics shutdown: %w", err)
	}

	select {
	case err := <-metricsErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("metrics server: %w", err)
		}
	default:
	}

	return runErr
}

// startReconcileSweep asks Temporal to start the long-running sweep driver
// under its fixed workflow ID. ALLOW_DUPLICATE + ErrorWhenAlreadyStarted makes
// this idempotent across worker restarts: if a sweep is already alive (this
// worker or another), the start collides and we log and move on — exactly one
// sweep runs for the whole stack, no matter how many times workers reboot.
func startReconcileSweep(ctx context.Context, a *app.App) error {
	a.Log.Info("starting reconcile sweep",
		"interval", a.Config.ReconcileSweepInterval,
		"workflowID", tfworkflow.ReconcileSweepWorkflowID)

	run, err := a.TC.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                                       tfworkflow.ReconcileSweepWorkflowID,
		TaskQueue:                                tfworkflow.TaskQueue,
		WorkflowIDReusePolicy:                    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, tfworkflow.ReconcileSweepWorkflow, tfworkflow.ReconcileSweepInput{
		Interval: a.Config.ReconcileSweepInterval,
	})
	if err != nil {
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &already) {
			a.Log.Info("reconcile sweep already running, skipping start")
			return nil
		}
		return fmt.Errorf("start reconcile sweep: %w", err)
	}

	a.Log.Info("reconcile sweep started", "runID", run.GetID())
	return nil
}
