package router

import (
	"log/slog"
	"net/http"

	"github.com/2SSK/tenantflow/internal/auth"
	"github.com/2SSK/tenantflow/internal/handler"
)

func New(tc handler.WorkflowStarter, store handler.TenantStore, auditStore handler.AuditStore, authProvider *auth.Provider, log *slog.Logger) *http.ServeMux {
	root := http.NewServeMux()

	// Public endpoints
	root.HandleFunc("GET /status", handler.Status)

	// All /api/* routes require authentication
	protected := http.NewServeMux()

	registerTenantRoutes(protected, tc, store, auditStore, log)

	root.Handle("/api/", auth.RequireAuth(authProvider, protected))

	return root
}
