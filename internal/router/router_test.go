package router

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/2SSK/tenantflow/internal/auth"
	"github.com/2SSK/tenantflow/internal/cost"
	"github.com/2SSK/tenantflow/internal/model"
	"go.temporal.io/sdk/client"
)

// auth.TokenVerifier stub: only "valid-token" verifies, returning the claims
// configured per test (operator roles by default, admin when requested). It
// lets the router tests exercise authorization without a running Keycloak.
type stubVerifier struct {
	validToken string
	claims     *auth.Claims
	err        error
}

func (s *stubVerifier) VerifyToken(_ context.Context, token string) (*auth.Claims, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.validToken != "" && token != s.validToken {
		return nil, errors.New("token does not match stub")
	}
	return s.claims, nil
}

func newStubVerifier(roles ...string) *stubVerifier {
	claims := auth.Claims{Sub: "user-1"}
	claims.RealmAccess.Roles = roles
	return &stubVerifier{validToken: "valid-token", claims: &claims}
}

// Minimal store/backend stubs satisfying the handler interfaces. Read
// handlers get deterministic data; DeleteTenant needs an active tenant so the
// workflow start path returns 202.
type stubStore struct {
	tenant *model.Tenant
	err    error
}

func (s *stubStore) GetTenant(ctx context.Context, tenantID string) (*model.Tenant, error) {
	return s.tenant, s.err
}

func (s *stubStore) ListTenants(ctx context.Context) ([]model.Tenant, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.tenant != nil {
		return []model.Tenant{*s.tenant}, nil
	}
	return []model.Tenant{}, nil
}

func (s *stubStore) TenantResources(ctx context.Context, tenantID string) (cost.Resources, error) {
	return cost.Resources{}, nil
}

type stubAuditStore struct{}

func (s *stubAuditStore) WriteEvent(ctx context.Context, event *model.AuditEvent) error { return nil }
func (s *stubAuditStore) ListEvents(ctx context.Context, tenantID string) ([]model.AuditEvent, error) {
	return []model.AuditEvent{}, nil
}

type stubBackupStore struct{}

func (s *stubBackupStore) ListBackups(ctx context.Context, tenantID string) ([]model.Backup, error) {
	return []model.Backup{}, nil
}

type stubFailedRunStore struct{}

func (s *stubFailedRunStore) ListFailed(ctx context.Context, limit int) ([]model.WorkflowInstance, error) {
	return []model.WorkflowInstance{}, nil
}
func (s *stubFailedRunStore) FindLatestFailed(ctx context.Context, tenantID, workflowType string) (model.WorkflowInstance, error) {
	return model.WorkflowInstance{}, nil
}

type stubStarter struct{}

func (s *stubStarter) ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow any, args ...any) (client.WorkflowRun, error) {
	return &fakeRun{id: options.ID}, nil
}
func (s *stubStarter) SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg any) error {
	return nil
}

// fakeRun implements client.WorkflowRun minimally.
type fakeRun struct{ id string }

func (f *fakeRun) GetID() string                               { return f.id }
func (f *fakeRun) GetRunID() string                            { return "run-1" }
func (f *fakeRun) GetFirstExecutionRunID() string              { return "run-1" }
func (f *fakeRun) Get(ctx context.Context, valuePtr any) error { return nil }
func (f *fakeRun) GetWithOptions(ctx context.Context, valuePtr any, options client.WorkflowRunGetOptions) error {
	return nil
}

func newTestRouter(verifier auth.TokenVerifier) http.Handler {
	store := &stubStore{
		tenant: &model.Tenant{
			TenantID:      "acme",
			Status:        model.TenantStatusActive,
			IsolationMode: model.IsolationModeDedicated,
		},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(&stubStarter{}, store, &stubAuditStore{}, &stubBackupStore{}, &stubFailedRunStore{}, verifier, log)
}

func doRequest(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type route struct {
	name   string
	method string
	path   string
}

// readRoutes are open to any authenticated platform user.
var readRoutes = []route{
	{"list tenants", "GET", "/api/v1/tenants"},
	{"tenant detail", "GET", "/api/v1/tenants/acme"},
	{"tenant events", "GET", "/api/v1/tenants/acme/events"},
	{"tenant backups", "GET", "/api/v1/tenants/acme/backups"},
	{"tenant cost", "GET", "/api/v1/tenants/acme/cost"},
}

// adminRoutes (all mutations + the failed-runs DLQ) additionally require the
// platform-admin role.
var adminRoutes = []route{
	{"create tenant", "POST", "/api/v1/tenants"},
	{"delete tenant", "DELETE", "/api/v1/tenants/acme"},
	{"cancel delete", "POST", "/api/v1/tenants/acme/cancel-delete"},
	{"upgrade tenant", "POST", "/api/v1/tenants/acme/upgrade"},
	{"migrate tenant", "POST", "/api/v1/tenants/acme/migrate"},
	{"backup tenant", "POST", "/api/v1/tenants/acme/backup"},
	{"restore tenant", "POST", "/api/v1/tenants/acme/restore"},
	{"reconcile tenant", "POST", "/api/v1/tenants/acme/reconcile"},
	{"retry tenant", "POST", "/api/v1/tenants/acme/retry"},
	{"list failed runs", "GET", "/api/v1/failed-runs"},
}

// TestRouter_PublicStatusNoAuth — GET /status is the only route reachable
// without credentials; load balancers can probe liveness before login.
func TestRouter_PublicStatusNoAuth(t *testing.T) {
	h := newTestRouter(newStubVerifier("platform-operator"))
	rec := doRequest(t, h, "GET", "/status", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /status without token = %d, want 200", rec.Code)
	}
}

// TestRouter_UnauthenticatedReadsRejected — every tenant read requires a
// token; without one the API answers 401 before touching a handler.
func TestRouter_UnauthenticatedReadsRejected(t *testing.T) {
	h := newTestRouter(newStubVerifier())
	for _, rr := range readRoutes {
		t.Run(rr.name, func(t *testing.T) {
			rec := doRequest(t, h, rr.method, rr.path, "", "")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s without token = %d, want 401", rr.method, rr.path, rec.Code)
			}
		})
	}
}

// TestRouter_InvalidTokenReadsRejected — a bearer token that fails
// verification is indistinguishable from no token.
func TestRouter_InvalidTokenReadsRejected(t *testing.T) {
	h := newTestRouter(newStubVerifier("platform-operator"))
	for _, rr := range []route{readRoutes[0], readRoutes[1]} {
		t.Run(rr.name, func(t *testing.T) {
			rec := doRequest(t, h, rr.method, rr.path, "garbage-token", "")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s with invalid token = %d, want 401", rr.method, rr.path, rec.Code)
			}
		})
	}
}

// TestRouter_AuthenticatedReadsAllowed — a valid token may read every tenant
// endpoint. The token is an operator (no platform-admin), which doubles as
// proof that reads do not require the admin role: the platform-operator role
// is the read-only tier.
func TestRouter_AuthenticatedReadsAllowed(t *testing.T) {
	h := newTestRouter(newStubVerifier("platform-operator"))
	for _, rr := range readRoutes {
		t.Run(rr.name, func(t *testing.T) {
			rec := doRequest(t, h, rr.method, rr.path, "valid-token", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("%s %s with operator token = %d, want 200 (body: %s)",
					rr.method, rr.path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestRouter_UnauthenticatedMutationsRejected — mutations and the DLQ are
// gated on a token first, so a credential-less request is 401 (not 403).
func TestRouter_UnauthenticatedMutationsRejected(t *testing.T) {
	h := newTestRouter(newStubVerifier())
	for _, rr := range adminRoutes {
		t.Run(rr.name, func(t *testing.T) {
			rec := doRequest(t, h, rr.method, rr.path, "", "")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s without token = %d, want 401", rr.method, rr.path, rec.Code)
			}
		})
	}
}

// TestRouter_NonAdminMutationForbidden — an authenticated operator (no
// platform-admin) is rejected with 403 on every mutation and on the
// failed-runs DLQ: reads are for everyone, writes for admins.
func TestRouter_NonAdminMutationForbidden(t *testing.T) {
	h := newTestRouter(newStubVerifier("platform-operator"))
	for _, rr := range adminRoutes {
		t.Run(rr.name, func(t *testing.T) {
			rec := doRequest(t, h, rr.method, rr.path, "valid-token", "")
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s with operator token = %d, want 403", rr.method, rr.path, rec.Code)
			}
		})
	}
}

// TestRouter_AdminMutationsAccepted — with the platform-admin role, a create
// returns 202 (workflow started) and a delete of an active tenant returns 202
// (grace period started).
func TestRouter_AdminMutationsAccepted(t *testing.T) {
	h := newTestRouter(newStubVerifier("platform-admin"))

	rec := doRequest(t, h, "POST", "/api/v1/tenants", "valid-token", `{"tenantID":"acme","isolationMode":"dedicated"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/v1/tenants with admin token = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, h, "DELETE", "/api/v1/tenants/acme", "valid-token", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("DELETE /api/v1/tenants/acme with admin token = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestRouter_FailedRunsRequiresAdmin — the failed-runs DLQ is the recovery
// lever into the whole system, so reading it is admin-only even though it is
// technically a read. A non-admin gets 403; an admin gets 200.
func TestRouter_FailedRunsRequiresAdmin(t *testing.T) {
	operator := newTestRouter(newStubVerifier("platform-operator"))
	rec := doRequest(t, operator, "GET", "/api/v1/failed-runs", "valid-token", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /api/v1/failed-runs with operator token = %d, want 403", rec.Code)
	}

	admin := newTestRouter(newStubVerifier("platform-admin"))
	rec = doRequest(t, admin, "GET", "/api/v1/failed-runs", "valid-token", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/failed-runs with admin token = %d, want 200", rec.Code)
	}
}
