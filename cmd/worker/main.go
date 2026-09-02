package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/2SSK/tenantflow/internal/app"
	"github.com/2SSK/tenantflow/internal/chaos"
	"github.com/2SSK/tenantflow/internal/metrics"
	tfworker "github.com/2SSK/tenantflow/internal/worker"
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
