package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/2SSK/tenantflow/internal/app"
	"github.com/2SSK/tenantflow/internal/chaos"
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

	a, err := app.New(ctx, "tenantflow worker")
	if err != nil {
		return err
	}
	defer a.Close()

	w := tfworker.New(a.TC, a.Repo, a.AuditRepo, a.BackupRepo, a.Provider, a.Identity,
		a.InstanceRepo,
		chaos.NewController(a.Config.Chaos.Rate, a.Config.Chaos.Activities), a.Log)

	return w.Run()
}
