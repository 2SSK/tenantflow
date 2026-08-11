package router

import (
	"log/slog"
	"net/http"

	"github.com/2SSK/tenantflow/internal/handler"
)

func New(tc handler.WorkflowStarter, log *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /status", handler.Status)

	tenants := handler.NewTenantHandler(tc, log)
	mux.HandleFunc("POST /api/v1/tenants", tenants.CreateTenant)

	return mux
}
