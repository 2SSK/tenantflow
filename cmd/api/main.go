package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/2SSK/tenantflow/internal/app"
	"github.com/2SSK/tenantflow/internal/cost"
	"github.com/2SSK/tenantflow/internal/metrics"
	"github.com/2SSK/tenantflow/internal/middleware"
	"github.com/2SSK/tenantflow/internal/router"
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
	a, err := app.New(ctx, "tenantflow api", metrics.NewTemporalHandler(reg))
	if err != nil {
		return err
	}
	defer a.Close()

	// Per-tenant cost collector: measures every tenant's resources and
	// applies the price model at every scrape, so deleted tenants can never
	// leave stale cost series behind in Prometheus.
	costLoader := cost.Loader(func(ctx context.Context) ([]cost.TenantEstimate, error) {
		tenants, err := a.Repo.ListTenantResources(ctx)
		if err != nil {
			return nil, err
		}
		estimates := make([]cost.TenantEstimate, 0, len(tenants))
		for _, t := range tenants {
			estimates = append(estimates, cost.TenantEstimate{
				TenantID:  t.TenantID,
				Resources: t.Resources,
				Estimate:  cost.MonthlyEstimate(t.Resources, cost.DefaultModel),
			})
		}
		return estimates, nil
	})
	reg.MustRegister(cost.NewCollector(costLoader, a.Log))

	mux := router.New(a.TC, a.Repo, a.AuditRepo, a.BackupRepo, a.InstanceRepo, a.Auth, a.Log)
	mux.Handle("GET /metrics", reg.Handler())

	srv := &http.Server{
		Addr: fmt.Sprintf(":%d", a.Config.HTTPPort),
		// Metrics is outermost so every request is counted; RequestLogger
		// inside keeps the story-log for each request.
		Handler:           middleware.Metrics(reg, middleware.RequestLogger(a.Log, mux)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		a.Log.Info("listening", "addr", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return fmt.Errorf("server: %w", err)
	case sig := <-stopCh:
		a.Log.Info("shutdown requested", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	a.Log.Info("shutdown complete")
	return nil
}
