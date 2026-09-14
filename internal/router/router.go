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
//
// Authorization model:
//   - GET /status stays public — it is the only route a load balancer may
//     probe without credentials.
//   - Every tenant read (list, detail, events, backups, cost) requires a
//     valid bearer token. Reads are open to any authenticated platform user;
//     with the realm's "platform-operator" role that is the read-only tier.
//   - Every mutation and the failed-runs DLQ additionally require the
//     "platform-admin" role, enforced by RequireRole.
func New(tc handler.WorkflowStarter, store handler.TenantStore, auditStore handler.AuditStore, backupStore handler.BackupStore, failedRuns handler.FailedRunStore, authProvider auth.TokenVerifier, log *slog.Logger) *http.ServeMux {
	root := http.NewServeMux()

	// Public endpoints — liveness/readiness only.
	root.HandleFunc("GET /status", handler.Status)

	tenants := handler.NewTenantHandler(tc, store, auditStore, backupStore, failedRuns, log)

	// Read-only API routes — token required, any authenticated platform user.
	reader := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(authProvider, h)
	}
	root.Handle("GET /api/v1/tenants", reader(tenants.ListTenants))
	root.Handle("GET /api/v1/tenants/{tenantID}", reader(tenants.GetTenant))
	root.Handle("GET /api/v1/tenants/{tenantID}/events", reader(tenants.ListEvents))
	root.Handle("GET /api/v1/tenants/{tenantID}/backups", reader(tenants.ListBackups))
	root.Handle("GET /api/v1/tenants/{tenantID}/cost", reader(tenants.CostTenant))

	// Mutating API routes — auth first (token), then the platform-admin role.
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

	// DLQ + recovery routes — operator-sensitive, so admin-only like mutations.
	root.Handle("GET /api/v1/failed-runs", admin(tenants.ListFailedRuns))
	root.Handle("POST /api/v1/tenants/{tenantID}/retry", admin(tenants.RetryTenant))

	return root
}
