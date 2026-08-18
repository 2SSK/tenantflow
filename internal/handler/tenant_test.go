package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/2SSK/tenantflow/internal/model"
	"github.com/2SSK/tenantflow/internal/repository"
	tfworkflow "github.com/2SSK/tenantflow/internal/workflow"
)

type stubWorkflowStarter struct {
	startedOptions client.StartWorkflowOptions
	startedArgs    []any
	err            error
}

type stubTenantStore struct {
	tenant  *model.Tenant
	tenants []model.Tenant
	err     error
}

type stubAuditStore struct{}

func (s *stubWorkflowStarter) ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow any, args ...any) (client.WorkflowRun, error) {
	s.startedOptions = options
	s.startedArgs = args
	if s.err != nil {
		return nil, s.err
	}
	return &fakeRun{id: options.ID}, nil
}

// fakeRun implements client.WorkflowRun minimally.
type fakeRun struct{ id string }

func (f *fakeRun) GetID() string                               { return f.id }
func (f *fakeRun) GetRunID() string                            { return "run-1" }
func (f *fakeRun) Get(ctx context.Context, valuePtr any) error { return nil }
func (f *fakeRun) GetWithOptions(ctx context.Context, valuePtr any, options client.WorkflowRunGetOptions) error {
	return nil
}

func (s *stubTenantStore) GetTenant(ctx context.Context, tenantID string) (*model.Tenant, error) {
	return s.tenant, s.err
}

func (s *stubTenantStore) ListTenants(ctx context.Context) ([]model.Tenant, error) {
	return s.tenants, s.err
}

func newTestTenantHandler(s *stubWorkflowStarter, store TenantStore) *TenantHandler {
	return NewTenantHandler(s, store, &stubAuditStore{}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
}

func TestCreateTenatAccept(t *testing.T) {
	stub := &stubWorkflowStarter{}
	h := newTestTenantHandler(stub, &stubTenantStore{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewBufferString(`{"tenantID":"acme"}`))
	rec := httptest.NewRecorder()

	h.CreateTenant(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var got CreateTenantResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.WorkflowID != "provision-acme" {
		t.Errorf("expected workflowID provision-acme, got %q", got.WorkflowID)
	}
	if got.Status != "provisioning" {
		t.Errorf("expected status provisioning, got %q", got.Status)
	}

	// Prove the handler wired the right  task queue and workflow input.
	if stub.startedOptions.TaskQueue != tfworkflow.TaskQueue {
		t.Errorf("unexpected task queue %v", &stub.startedOptions.TaskQueue)
	}
	if stub.startedOptions.WorkflowIDReusePolicy != enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE {
		t.Errorf("expected REJECT_DUPLICATE reuse policy, got %v", &stub.startedOptions.WorkflowIDReusePolicy)
	}
	if !stub.startedOptions.WorkflowExecutionErrorWhenAlreadyStarted {
		t.Errorf("expected WorkflowExecutionErrorWhenAlreadyStarted to be true")
	}
	in, ok := stub.startedArgs[0].(tfworkflow.ProvisionInput)
	if !ok || in.TenantID != "acme" {
		t.Errorf("unexpected workflow input: %#v", stub.startedArgs)
	}
}

func TestCreateTenantBadJSON(t *testing.T) {
	stub := &stubWorkflowStarter{}
	h := newTestTenantHandler(stub, &stubTenantStore{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewBufferString(`{not json`))
	rec := httptest.NewRecorder()

	h.CreateTenant(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateTenatMissingTenantID(t *testing.T) {
	stub := &stubWorkflowStarter{}
	h := newTestTenantHandler(stub, &stubTenantStore{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewBufferString(`{"tenantID":""}`))
	rec := httptest.NewRecorder()

	h.CreateTenant(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if stub.startedOptions.ID != "" {
		t.Fatalf("workflow should not have been started, but got ID %q", stub.startedOptions.ID)
	}
}

func TestCreateTenatConflict(t *testing.T) {
	stub := &stubWorkflowStarter{
		err: serviceerror.NewWorkflowExecutionAlreadyStarted("already started", "provision-acme", "run-1"),
	}

	h := newTestTenantHandler(stub, &stubTenantStore{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewBufferString(`{"tenantID":"acme"}`))
	rec := httptest.NewRecorder()

	h.CreateTenant(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
}

func TestGetTenant(t *testing.T) {
	now := time.Now()
	workflowID := "provision-acme"
	existing := &model.Tenant{
		TenantID:   "acme",
		Status:     model.TenantStatusActive,
		WorkflowID: &workflowID,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	tests := []struct {
		name       string
		store      *stubTenantStore
		wantStatus int
		wantBody   string
	}{
		{
			name:       "found",
			store:      &stubTenantStore{tenant: existing},
			wantStatus: http.StatusOK,
			wantBody:   `"tenantID":"acme"`,
		},
		{
			name:       "workflow id is included",
			store:      &stubTenantStore{tenant: existing},
			wantStatus: http.StatusOK,
			wantBody:   `"workflowID":"provision-acme"`,
		},
		{
			name:       "not found",
			store:      &stubTenantStore{err: repository.ErrNotFound},
			wantStatus: http.StatusNotFound,
			wantBody:   `"error":"tenant not found"`,
		},
		{
			name:       "store error",
			store:      &stubTenantStore{err: errors.New("connection refused")},
			wantStatus: http.StatusInternalServerError,
			wantBody:   `"error":"failed to get tenant"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestTenantHandler(&stubWorkflowStarter{}, tt.store)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/acme", nil)
			req.SetPathValue("tenantID", "acme")
			rec := httptest.NewRecorder()

			h.GetTenant(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want to contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestListTenants(t *testing.T) {
	tests := []struct {
		name       string
		store      *stubTenantStore
		wantStatus int
		wantBody   string
	}{
		{
			name: "returns all tenants",
			store: &stubTenantStore{
				tenants: []model.Tenant{
					{TenantID: "acme", Status: model.TenantStatusActive},
					{TenantID: "globex", Status: model.TenantStatusDeleted},
				},
			},
			wantStatus: http.StatusOK,
			wantBody:   `"tenantID":"acme","status":"active"`,
		},
		{
			name:       "empty store returns empty list not null",
			store:      &stubTenantStore{},
			wantStatus: http.StatusOK,
			wantBody:   `"tenants":[]`,
		},
		{
			name:       "store error",
			store:      &stubTenantStore{err: errors.New("connection refused")},
			wantStatus: http.StatusInternalServerError,
			wantBody:   `"error":"failed to list tenants"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubWorkflowStarter{}
			h := newTestTenantHandler(stub, tt.store)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants", nil)
			rec := httptest.NewRecorder()

			h.ListTenants(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want to contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestDeleteTenant(t *testing.T) {
	workflowID := "provision-acme"
	existing := &model.Tenant{
		TenantID:   "acme",
		Status:     model.TenantStatusActive,
		WorkflowID: &workflowID,
	}

	tests := []struct {
		name       string
		store      *stubTenantStore
		starterErr error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "accepted",
			store:      &stubTenantStore{tenant: existing},
			wantStatus: http.StatusAccepted,
			wantBody:   `"workflowID":"deprovision-acme","status":"deleting"`,
		},
		{
			name:       "not found",
			store:      &stubTenantStore{err: repository.ErrNotFound},
			wantStatus: http.StatusNotFound,
			wantBody:   `"error":"tenant not found"`,
		},
		{
			name:       "already deleting",
			store:      &stubTenantStore{tenant: existing},
			starterErr: serviceerror.NewWorkflowExecutionAlreadyStarted("already started", "deprovision-acme", "run-1"),
			wantStatus: http.StatusConflict,
			wantBody:   `"error":"deletion already in progress for this tenant"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubWorkflowStarter{err: tt.starterErr}
			h := newTestTenantHandler(stub, tt.store)

			req := httptest.NewRequest(http.MethodDelete, "/api/v1/tenants/acme", nil)
			req.SetPathValue("tenantID", "acme")
			rec := httptest.NewRecorder()

			h.DeleteTenant(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want to contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func (s *stubAuditStore) ListEvents(ctx context.Context, tenantID string) ([]model.AuditEvent, error) {
	return nil, nil
}
