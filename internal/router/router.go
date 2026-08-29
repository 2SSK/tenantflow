package router

import (
	"log/slog"
	"net/http"

	"github.com/2SSK/tenantflow/internal/auth"
	"github.com/2SSK/tenantflow/internal/handler"
)

func New(tc handler.WorkflowStarter, store handler.TenantStore, auditStore handler.AuditStore, backupStore handler.BackupStore, authProvider *auth.Provider, log *slog.Logger) *http.ServeMux {
	root := http.NewServeMux()

	// Public endpoints
	root.HandleFunc("GET /status", handler.Status)

	tenants := handler.NewTenantHandler(tc, store, auditStore, backupStore, log)

	// Read-only API routes — no auth required
	root.HandleFunc("GET /api/v1/tenants", tenants.ListTenants)
	root.HandleFunc("GET /api/v1/tenants/{tenantID}", tenants.GetTenant)
	root.HandleFunc("GET /api/v1/tenants/{tenantID}/events", tenants.ListEvents)
	root.HandleFunc("GET /api/v1/tenants/{tenantID}/backups", tenants.ListBackups)

	// Mutating API routes — require authentication + role
	protected := http.NewServeMux()
	protected.Handle("POST /api/v1/tenants", auth.RequireRole("platform-admin", http.HandlerFunc(tenants.CreateTenant)))
	protected.Handle("DELETE /api/v1/tenants/{tenantID}", auth.RequireRole("platform-admin", http.HandlerFunc(tenants.DeleteTenant)))
	protected.Handle("POST /api/v1/tenants/{tenantID}/upgrade", auth.RequireRole("platform-admin", http.HandlerFunc(tenants.UpgradeTenant)))
	protected.Handle("POST /api/v1/tenants/{tenantID}/migrate", auth.RequireRole("platform-admin", http.HandlerFunc(tenants.MigrateTenant)))
	protected.Handle("POST /api/v1/tenants/{tenantID}/backup", auth.RequireRole("platform-admin", http.HandlerFunc(tenants.BackupTenant)))
	protected.Handle("POST /api/v1/tenants/{tenantID}/restore", auth.RequireRole("platform-admin", http.HandlerFunc(tenants.RestoreTenant)))
	root.Handle("/api/", auth.RequireAuth(authProvider, protected))

	return root
}
