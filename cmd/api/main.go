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

	"github.com/joho/godotenv"

	"github.com/2SSK/tenantflow/internal/config"
	"github.com/2SSK/tenantflow/internal/database"
	"github.com/2SSK/tenantflow/internal/logger"
	"github.com/2SSK/tenantflow/internal/middleware"
	"github.com/2SSK/tenantflow/internal/router"
	"github.com/2SSK/tenantflow/internal/temporal"
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
	log.Info("tenantflow api starting", "env", cfg.Env, "port", cfg.HTTPPort)

	tc, err := temporal.New(ctx, cfg, log)
	if err != nil {
		return fmt.Errorf("temporal: %w", err)
	}
	defer tc.Close()
	log.Info("temporal connected", "address", cfg.TemporalAddress, "namespace", cfg.TemporalNamespace)

	db, err := database.New(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close()

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           middleware.RequestLogger(log, router.New(tc, log)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return fmt.Errorf("server: %w", err)
	case sig := <-stopCh:
		log.Info("shutdown requested", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	log.Info("shutdown complete")
	return nil
}
