package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/2SSK/tenantflow/internal/app"
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

	w := tfworker.New(a.TC, a.Repo, a.AuditRepo, a.Provider, a.Identity, a.Log)

	return w.Run()
}
