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
	"github.com/2SSK/tenantflow/internal/repository"
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

	mux := router.New(a.TC, a.Repo, a.AuditRepo, a.BackupRepo, a.InstanceRepo, repository.NewPostgresQuotaStore(a.DB.Pool), a.Auth, a.Log)
	mux.Handle("GET /metrics", reg.Handler())

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", a.Config.HTTPPort),
		Handler:           middleware.Metrics(reg, middleware.RequestLogger(a.Log, mux)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	return serveHTTP(ctx, srv, a.Log)
}

// shutdownTimeout bounds how long a graceful shutdown waits for in-flight
// requests to finish before giving up and returning an error.
const shutdownTimeout = 10 * time.Second

// serveHTTP runs the server until it fails on its own or a termination
// signal arrives. On a signal it stops accepting new connections and drains
// in-flight requests within shutdownTimeout — a crash here would drop
// requests that were already being processed.
func serveHTTP(ctx context.Context, srv *http.Server, log *slog.Logger) error {
	// ListenAndServe blocks until the server fails, so it runs on its own
	// goroutine and reports back through a channel.
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()

	// The process waits on either the server dying on its own (errCh) or an
	// operator sending a termination signal (stopCh).
	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stopCh)

	select {
	case err := <-errCh:
		return fmt.Errorf("server: %w", err)
	case sig := <-stopCh:
		log.Info("shutdown requested", "signal", sig.String())
	}

	// Graceful shutdown: Shutdown stops accepting NEW connections and blocks
	// until in-flight requests finish or the deadline fires, whichever first.
	shutdownCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	log.Info("shutdown complete")
	return nil
}
