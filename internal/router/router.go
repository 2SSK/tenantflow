package router

import (
	"log/slog"
	"net/http"

	"github.com/2SSK/tenantflow/internal/auth"
	"github.com/2SSK/tenantflow/internal/handler"
)

// New builds the API's HTTP routing table on a single flat ServeMux.
//
// Go 1.22 patterns disambiguate by method, so protected routes can live
// next to public ones on the same mux instead of hiding behind a nested
// catch-all ("/api/"). That matters for metrics: r.Pattern — used by the
// metrics middleware to label requests — is only precise when the route
// matched on THIS mux, so a flat table yields labels like
// "POST /api/v1/tenants" instead of the coarse "/api/".
func New(tc handler.WorkflowStarter, store handler.TenantStore, auditStore handler.AuditStore, backupStore handler.BackupStore, failedRuns handler.FailedRunStore, authProvider *auth.Provider, log *slog.Logger) *http.ServeMux {
	root := http.NewServeMux()

	// Public endpoints
	root.HandleFunc("GET /status", handler.Status)

	tenants := handler.NewTenantHandler(tc, store, auditStore, backupStore, failedRuns, log)

	// Read-only API routes — no auth required
	root.HandleFunc("GET /api/v1/tenants", tenants.ListTenants)
	root.HandleFunc("GET /api/v1/tenants/{tenantID}", tenants.GetTenant)
	root.HandleFunc("GET /api/v1/tenants/{tenantID}/events", tenants.ListEvents)
	root.HandleFunc("GET /api/v1/tenants/{tenantID}/backups", tenants.ListBackups)
	root.HandleFunc("GET /api/v1/tenants/{tenantID}/cost", tenants.CostTenant)

	// Mutating API routes — auth first (token), then role.
	admin := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(authProvider, auth.RequireRole("platform-admin", h))
	}
	root.Handle("POST /api/v1/tenants", admin(tenants.CreateTenant))
	root.Handle("DELETE /api/v1/tenants/{tenantID}", admin(tenants.DeleteTenant))
	root.Handle("POST /api/v1/tenants/{tenantID}/cancel-delete", admin(tenants.CancelTenantDelete))
	root.Handle("POST /api/v1/tenants/{tenantID}/upgrade", admin(tenants.UpgradeTenant))
	root.Handle("POST /api/v1/tenants/{tenantID}/migrate", admin(tenants.MigrateTenant))
	root.Handle("POST /api/v1/tenants/{tenantID}/backup", admin(tenants.BackupTenant))
	root.Handle("POST /api/v1/tenants/{tenantID}/restore", admin(tenants.RestoreTenant))
	root.Handle("POST /api/v1/tenants/{tenantID}/reconcile", admin(tenants.ReconcileTenant))

	// DLQ + recovery routes
	root.Handle("GET /api/v1/failed-runs", admin(tenants.ListFailedRuns))
	root.Handle("POST /api/v1/tenants/{tenantID}/retry", admin(tenants.RetryTenant))

	return root
}
