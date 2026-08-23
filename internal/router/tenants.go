package router

import (
	"log/slog"
	"net/http"

	"github.com/2SSK/tenantflow/internal/auth"
	"github.com/2SSK/tenantflow/internal/handler"
)

func registerTenantRoutes(mux *http.ServeMux, tc handler.WorkflowStarter, store handler.TenantStore, auditStore handler.AuditStore, log *slog.Logger) {
	tenants := handler.NewTenantHandler(tc, store, auditStore, log)

	// Read operations - no authentication required
	mux.HandleFunc("GET /api/v1/tenants", tenants.ListTenants)
	mux.HandleFunc("GET /api/v1/tenants/{tenantID}", tenants.GetTenant)
	mux.HandleFunc("GET /api/v1/tenants/{tenantID}/events", tenants.ListEvents)

	// Write operations - require authentication
	mux.Handle("POST /api/v1/tenants", auth.RequireRole("platform-admin", http.HandlerFunc(tenants.CreateTenant)))
	mux.Handle("DELETE /api/v1/tenants/{tenantID}", auth.RequireRole("platform-admin", http.HandlerFunc(tenants.DeleteTenant)))
}
