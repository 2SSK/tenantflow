package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/joho/godotenv"

	"github.com/2SSK/tenantflow/internal/config"
	"github.com/2SSK/tenantflow/internal/database"
	"github.com/2SSK/tenantflow/internal/logger"
	"github.com/2SSK/tenantflow/internal/repository"
	"github.com/2SSK/tenantflow/internal/temporal"
)

type App struct {
	Config    config.Config
	Log       *slog.Logger
	TC        *temporal.Client
	DB        *database.DB
	Repo      *repository.PostgresTenantRepository
	AuditRepo *repository.PostgresAuditRepository
}

func New(ctx context.Context, process string) (*App, error) {
	if err := godotenv.Load(); err != nil {
		slog.Warn("No .env file found, relying on environment", "error", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	log := logger.New(cfg.Env, os.Stdout)
	slog.SetDefault(log)
	log.Info(process+" starting", "env", cfg.Env)

	tc, err := temporal.New(ctx, cfg, log)
	if err != nil {
		return nil, fmt.Errorf("temporal: %w", err)
	}
	log.Info("temporal connected", "address", cfg.TemporalAddress, "namespace", cfg.TemporalNamespace)

	db, err := database.New(ctx, cfg.DatabaseURL, log)
	if err != nil {
		tc.Close()
		return nil, fmt.Errorf("database: %w", err)
	}

	repo := repository.NewPostgresTenantRepository(db.Pool)
	auditRepo := repository.NewPostgresAuditRepository(db.Pool)

	return &App{
		Config:    cfg,
		Log:       log,
		TC:        tc,
		DB:        db,
		Repo:      repo,
		AuditRepo: auditRepo,
	}, nil
}

// Close releases resources in reverse creation order.
func (a *App) Close() {
	a.DB.Close()
	a.TC.Close()
}
