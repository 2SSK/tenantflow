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

	a, err := app.New(ctx, "tenantflow api")
	if err != nil {
		return err
	}
	defer a.Close()

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", a.Config.HTTPPort),
		Handler:           middleware.RequestLogger(a.Log, router.New(a.TC, a.Repo, a.AuditRepo, a.Auth, a.Log)),
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
