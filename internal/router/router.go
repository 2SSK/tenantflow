package router

import (
	"log/slog"
	"net/http"

	"github.com/2SSK/tenantflow/internal/handler"
)

func New(tc handler.WorkflowStarter, store handler.TenantStore, auditStore handler.AuditStore, log *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /status", handler.Status)

	tenants := handler.NewTenantHandler(tc, store, auditStore, log)
	mux.HandleFunc("POST /api/v1/tenants", tenants.CreateTenant)
	mux.HandleFunc("GET /api/v1/tenants", tenants.ListTenants)
	mux.HandleFunc("GET /api/v1/tenants/{tenantID}", tenants.GetTenant)
	mux.HandleFunc("DELETE /api/v1/tenants/{tenantID}", tenants.DeleteTenant)
	mux.HandleFunc("GET /api/v1/tenants/{tenantID}/events", tenants.ListEvents)

	return mux
}
