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
	// signal captures the last SignalWorkflow call (workflowID, signalName).
	signalWorkflowID string
	signalName       string
	signalErr        error
}

type stubTenantStore struct {
	tenant  *model.Tenant
	tenants []model.Tenant
	err     error
}

type stubAuditStore struct {
	events []model.AuditEvent
}

func (s *stubAuditStore) WriteEvent(ctx context.Context, event *model.AuditEvent) error {
	s.events = append(s.events, *event)
	return nil
}

type stubBackupStore struct {
	backups []model.Backup
	err     error
}

type stubFailedRunStore struct {
	instances []model.WorkflowInstance
	err       error
	lastLimit int
}

func (s *stubFailedRunStore) ListFailed(ctx context.Context, limit int) ([]model.WorkflowInstance, error) {
	s.lastLimit = limit
	return s.instances, s.err
}

func (s *stubBackupStore) ListBackups(ctx context.Context, tenantID string) ([]model.Backup, error) {
	return s.backups, s.err
}

func (s *stubWorkflowStarter) ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow any, args ...any) (client.WorkflowRun, error) {
	s.startedOptions = options
	s.startedArgs = args
	if s.err != nil {
		return nil, s.err
	}
	return &fakeRun{id: options.ID}, nil
}

func (s *stubWorkflowStarter) SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg any) error {
	s.signalWorkflowID = workflowID
	s.signalName = signalName
	return s.signalErr
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
	return NewTenantHandler(s, store, &stubAuditStore{}, &stubBackupStore{}, &stubFailedRunStore{}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
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
			wantBody:   `"workflowID":"delete-acme","status":"deleting","gracePeriodDays":30`,
		},
		{
			name:       "not found",
			store:      &stubTenantStore{err: repository.ErrNotFound},
			wantStatus: http.StatusNotFound,
			wantBody:   `"error":"tenant not found"`,
		},
		{
			name:       "already deleting",
			store:      &stubTenantStore{tenant: &model.Tenant{TenantID: "acme", Status: model.TenantStatusDeleting}},
			wantStatus: http.StatusConflict,
			wantBody:   `"error":"deletion already in progress for this tenant"`,
		},
		{
			name:       "already deleted",
			store:      &stubTenantStore{tenant: &model.Tenant{TenantID: "acme", Status: model.TenantStatusDeleted}},
			wantStatus: http.StatusConflict,
			wantBody:   `"error":"tenant already deleted"`,
		},
		{
			name:       "already started",
			store:      &stubTenantStore{tenant: existing},
			starterErr: serviceerror.NewWorkflowExecutionAlreadyStarted("already started", "delete-acme", "run-1"),
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

func TestCancelTenantDelete(t *testing.T) {
	tests := []struct {
		name       string
		store      *stubTenantStore
		signalErr  error
		wantStatus int
		wantBody   string
		wantSignal bool
	}{
		{
			name:       "accepted",
			store:      &stubTenantStore{tenant: &model.Tenant{TenantID: "acme", Status: model.TenantStatusDeleting}},
			wantStatus: http.StatusAccepted,
			wantBody:   `"workflowID":"delete-acme","status":"cancelling"`,
			wantSignal: true,
		},
		{
			name:       "not found",
			store:      &stubTenantStore{err: repository.ErrNotFound},
			wantStatus: http.StatusNotFound,
			wantBody:   `"error":"tenant not found"`,
		},
		{
			name:       "not deleting",
			store:      &stubTenantStore{tenant: &model.Tenant{TenantID: "acme", Status: model.TenantStatusActive}},
			wantStatus: http.StatusConflict,
			wantBody:   `"error":"tenant is not being deleted"`,
		},
		{
			name:       "workflow not running",
			store:      &stubTenantStore{tenant: &model.Tenant{TenantID: "acme", Status: model.TenantStatusDeleting}},
			signalErr:  serviceerror.NewNotFound("workflow not found"),
			wantStatus: http.StatusConflict,
			wantBody:   `"error":"delete workflow is not running"`,
			wantSignal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubWorkflowStarter{signalErr: tt.signalErr}
			h := newTestTenantHandler(stub, tt.store)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/acme/cancel-delete", nil)
			req.SetPathValue("tenantID", "acme")
			rec := httptest.NewRecorder()

			h.CancelTenantDelete(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want to contain %q", rec.Body.String(), tt.wantBody)
			}
			if tt.wantSignal {
				if stub.signalWorkflowID != "delete-acme" {
					t.Errorf("signalled workflowID = %q, want delete-acme", stub.signalWorkflowID)
				}
				if stub.signalName != tfworkflow.CancelDeleteSignalName {
					t.Errorf("signal name = %q, want %q", stub.signalName, tfworkflow.CancelDeleteSignalName)
				}
			} else if stub.signalName != "" {
				t.Errorf("unexpected signal sent: workflowID=%q name=%q", stub.signalWorkflowID, stub.signalName)
			}
		})
	}
}

func TestUpgradeTenant(t *testing.T) {
	workflowID := "provision-acme"
	active := &model.Tenant{
		TenantID:   "acme",
		Status:     model.TenantStatusActive,
		WorkflowID: &workflowID,
	}
	provisioning := &model.Tenant{
		TenantID: "acme",
		Status:   model.TenantStatusProvisioning,
	}

	tests := []struct {
		name       string
		store      *stubTenantStore
		starterErr error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "active tenant accepted",
			store:      &stubTenantStore{tenant: active},
			wantStatus: http.StatusAccepted,
			wantBody:   `"workflowID":"upgrade-acme","status":"upgrading"`,
		},
		{
			name:       "not found",
			store:      &stubTenantStore{err: repository.ErrNotFound},
			wantStatus: http.StatusNotFound,
			wantBody:   `"error":"tenant not found"`,
		},
		{
			name:       "non-active tenant conflicts",
			store:      &stubTenantStore{tenant: provisioning},
			wantStatus: http.StatusConflict,
			wantBody:   `"error":"tenant must be active to upgrade"`,
		},
		{
			name:       "already upgrading conflicts",
			store:      &stubTenantStore{tenant: active},
			starterErr: serviceerror.NewWorkflowExecutionAlreadyStarted("already started", "upgrade-acme", "run-1"),
			wantStatus: http.StatusConflict,
			wantBody:   `"error":"upgrade already in progress for this tenant"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubWorkflowStarter{err: tt.starterErr}
			h := newTestTenantHandler(stub, tt.store)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/acme/upgrade", nil)
			req.SetPathValue("tenantID", "acme")
			rec := httptest.NewRecorder()

			h.UpgradeTenant(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want to contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestMigrateTenant(t *testing.T) {
	workflowID := "provision-acme"
	active := &model.Tenant{
		TenantID:   "acme",
		Status:     model.TenantStatusActive,
		WorkflowID: &workflowID,
	}
	provisioning := &model.Tenant{
		TenantID: "acme",
		Status:   model.TenantStatusProvisioning,
	}

	tests := []struct {
		name       string
		store      *stubTenantStore
		starterErr error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "active tenant accepted",
			store:      &stubTenantStore{tenant: active},
			wantStatus: http.StatusAccepted,
			wantBody:   `"workflowID":"migrate-acme","status":"migrating"`,
		},
		{
			name:       "not found",
			store:      &stubTenantStore{err: repository.ErrNotFound},
			wantStatus: http.StatusNotFound,
			wantBody:   `"error":"tenant not found"`,
		},
		{
			name:       "non-active tenant conflicts",
			store:      &stubTenantStore{tenant: provisioning},
			wantStatus: http.StatusConflict,
			wantBody:   `"error":"tenant must be active to migrate"`,
		},
		{
			name:       "already migrating conflicts",
			store:      &stubTenantStore{tenant: active},
			starterErr: serviceerror.NewWorkflowExecutionAlreadyStarted("already started", "migrate-acme", "run-1"),
			wantStatus: http.StatusConflict,
			wantBody:   `"error":"migration already in progress for this tenant"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubWorkflowStarter{err: tt.starterErr}
			h := newTestTenantHandler(stub, tt.store)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/acme/migrate", nil)
			req.SetPathValue("tenantID", "acme")
			rec := httptest.NewRecorder()

			h.MigrateTenant(rec, req)

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

func TestBackupTenant(t *testing.T) {
	workflowID := "provision-acme"
	active := &model.Tenant{
		TenantID:   "acme",
		Status:     model.TenantStatusActive,
		WorkflowID: &workflowID,
	}
	provisioning := &model.Tenant{
		TenantID: "acme",
		Status:   model.TenantStatusProvisioning,
	}

	tests := []struct {
		name       string
		store      *stubTenantStore
		starterErr error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "active tenant accepted",
			store:      &stubTenantStore{tenant: active},
			wantStatus: http.StatusAccepted,
			wantBody:   `"workflowID":"backup-acme","status":"backing-up"`,
		},
		{
			name:       "not found",
			store:      &stubTenantStore{err: repository.ErrNotFound},
			wantStatus: http.StatusNotFound,
			wantBody:   `"error":"tenant not found"`,
		},
		{
			name:       "non-active tenant conflicts",
			store:      &stubTenantStore{tenant: provisioning},
			wantStatus: http.StatusConflict,
			wantBody:   `"error":"tenant must be active to backup"`,
		},
		{
			name:       "already backing up conflicts",
			store:      &stubTenantStore{tenant: active},
			starterErr: serviceerror.NewWorkflowExecutionAlreadyStarted("already started", "backup-acme", "run-1"),
			wantStatus: http.StatusConflict,
			wantBody:   `"error":"backup already in progress for this tenant"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubWorkflowStarter{err: tt.starterErr}
			h := newTestTenantHandler(stub, tt.store)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/acme/backup", nil)
			req.SetPathValue("tenantID", "acme")
			rec := httptest.NewRecorder()

			h.BackupTenant(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want to contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestListBackups(t *testing.T) {
	done := time.Now()
	backups := []model.Backup{
		{ID: 1, TenantID: "acme", Filename: "acme_1.sql", Status: model.BackupStatusCompleted, CompletedAt: &done},
		{ID: 2, TenantID: "acme", Filename: "acme_2.sql", Status: model.BackupStatusFailed},
	}

	tests := []struct {
		name       string
		store      *stubTenantStore
		wantStatus int
		wantBody   string
	}{
		{
			name:       "lists backups",
			store:      &stubTenantStore{tenant: &model.Tenant{TenantID: "acme", Status: model.TenantStatusActive}},
			wantStatus: http.StatusOK,
			wantBody:   `"filename":"acme_1.sql"`,
		},
		{
			name:       "tenant not found",
			store:      &stubTenantStore{err: repository.ErrNotFound},
			wantStatus: http.StatusNotFound,
			wantBody:   `"error":"tenant not found"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubWorkflowStarter{}
			bs := &stubBackupStore{backups: backups, err: tt.store.err}
			h := NewTenantHandler(stub, tt.store, &stubAuditStore{}, bs, &stubFailedRunStore{}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/acme/backups", nil)
			req.SetPathValue("tenantID", "acme")
			rec := httptest.NewRecorder()

			h.ListBackups(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want to contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestRestoreTenant(t *testing.T) {
	workflowID := "provision-acme"
	active := &model.Tenant{
		TenantID:   "acme",
		Status:     model.TenantStatusActive,
		WorkflowID: &workflowID,
	}
	provisioning := &model.Tenant{
		TenantID: "acme",
		Status:   model.TenantStatusProvisioning,
	}

	validBody := func() *strings.Reader {
		return strings.NewReader(`{"backupID":7}`)
	}

	tests := []struct {
		name       string
		store      *stubTenantStore
		body       *strings.Reader
		starterErr error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "active tenant accepted",
			store:      &stubTenantStore{tenant: active},
			body:       validBody(),
			wantStatus: http.StatusAccepted,
			wantBody:   `"workflowID":"restore-acme","status":"restoring"`,
		},
		{
			name:       "missing backupID rejected",
			store:      &stubTenantStore{tenant: active},
			body:       strings.NewReader(`{}`),
			wantStatus: http.StatusBadRequest,
			wantBody:   `"error":"backupID is required"`,
		},
		{
			name:       "invalid JSON rejected",
			store:      &stubTenantStore{tenant: active},
			body:       strings.NewReader(`{`),
			wantStatus: http.StatusBadRequest,
			wantBody:   `"error":"invalid JSON body"`,
		},
		{
			name:       "not found",
			store:      &stubTenantStore{err: repository.ErrNotFound},
			body:       validBody(),
			wantStatus: http.StatusNotFound,
			wantBody:   `"error":"tenant not found"`,
		},
		{
			name:       "non-active tenant conflicts",
			store:      &stubTenantStore{tenant: provisioning},
			body:       validBody(),
			wantStatus: http.StatusConflict,
			wantBody:   `"error":"tenant must be active to restore"`,
		},
		{
			name:       "already restoring conflicts",
			store:      &stubTenantStore{tenant: active},
			body:       validBody(),
			starterErr: serviceerror.NewWorkflowExecutionAlreadyStarted("already started", "restore-acme", "run-1"),
			wantStatus: http.StatusConflict,
			wantBody:   `"error":"restore already in progress for this tenant"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubWorkflowStarter{err: tt.starterErr}
			h := newTestTenantHandler(stub, tt.store)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/acme/restore", tt.body)
			req.SetPathValue("tenantID", "acme")
			rec := httptest.NewRecorder()

			h.RestoreTenant(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want to contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestRetryTenant(t *testing.T) {
	failedTenant := &model.Tenant{TenantID: "acme", Status: model.TenantStatusFailed, IsolationMode: model.IsolationModeDedicated}
	activeTenant := &model.Tenant{TenantID: "acme", Status: model.TenantStatusActive}

	tests := []struct {
		name       string
		tenant     *model.Tenant
		storeErr   error
		startErr   error
		wantStatus int
		wantBody   string
		started    bool
	}{
		{
			name:       "starts provisioning for a failed tenant",
			tenant:     failedTenant,
			wantStatus: http.StatusAccepted,
			wantBody:   `"workflowID":"provision-acme"`,
			started:    true,
		},
		{
			name:       "conflict when tenant is not failed",
			tenant:     activeTenant,
			wantStatus: http.StatusConflict,
			wantBody:   "only failed tenants can be retried",
			started:    false,
		},
		{
			name:       "not found",
			storeErr:   repository.ErrNotFound,
			wantStatus: http.StatusNotFound,
			wantBody:   "tenant not found",
			started:    false,
		},
		{
			name:       "workflow start error",
			tenant:     failedTenant,
			startErr:   errors.New("temporal down"),
			wantStatus: http.StatusInternalServerError,
			wantBody:   "failed to restart provisioning workflow",
			started:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubWorkflowStarter{err: tt.startErr}
			audit := &stubAuditStore{}
			h := NewTenantHandler(stub, &stubTenantStore{tenant: tt.tenant, err: tt.storeErr}, audit, &stubBackupStore{}, &stubFailedRunStore{}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

			req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/acme/retry", nil)
			req.SetPathValue("tenantID", "acme")
			rec := httptest.NewRecorder()

			h.RetryTenant(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want to contain %q", rec.Body.String(), tt.wantBody)
			}
			workflowStarted := stub.startedOptions.ID != ""
			if workflowStarted != tt.started {
				t.Errorf("workflow started = %v, want %v", workflowStarted, tt.started)
			}
			if tt.started && tt.startErr == nil {
				if stub.startedOptions.ID != "provision-acme" {
					t.Errorf("workflow ID = %q, want provision-acme", stub.startedOptions.ID)
				}
				if stub.startedOptions.WorkflowIDReusePolicy != enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE {
					t.Errorf("reuse policy = %v, want ALLOW_DUPLICATE", stub.startedOptions.WorkflowIDReusePolicy)
				}
				if len(audit.events) != 1 || audit.events[0].EventType != model.AuditEventTenantReprovisionReq {
					t.Errorf("expected TENANT_REPROVISION_REQUESTED audit event, got %+v", audit.events)
				}
			}
		})
	}
}

func TestListFailedRuns(t *testing.T) {
	now := time.Now()
	msg := "chaos injection: simulated failure"
	tests := []struct {
		name       string
		instances  []model.WorkflowInstance
		storeErr   error
		query      string
		wantLimit  int
		wantStatus int
		wantBody   string
	}{
		{
			name: "lists failed runs",
			instances: []model.WorkflowInstance{{
				TenantID:     "acme",
				WorkflowType: "ProvisionTenantWorkflow",
				WorkflowID:   "provision-acme",
				RunID:        "run-1",
				Status:       "failed",
				Error:        &msg,
				StartedAt:    now,
				FinishedAt:   &now,
			}},
			wantLimit:  50,
			wantStatus: http.StatusOK,
			wantBody:   `"tenantID":"acme"`,
		},
		{
			name:       "honors small limit and returns empty list",
			query:      "limit=3",
			wantLimit:  3,
			wantStatus: http.StatusOK,
			wantBody:   `"runs":[]`,
		},
		{
			name:       "rejects oversized limit with default",
			query:      "limit=9999",
			wantLimit:  50,
			wantStatus: http.StatusOK,
			wantBody:   `"runs":[]`,
		},
		{
			name:       "store error",
			storeErr:   errors.New("db down"),
			wantLimit:  50,
			wantStatus: http.StatusInternalServerError,
			wantBody:   "failed to list failed runs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &stubFailedRunStore{instances: tt.instances, err: tt.storeErr}
			h := NewTenantHandler(&stubWorkflowStarter{}, &stubTenantStore{}, &stubAuditStore{}, &stubBackupStore{}, store, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/failed-runs"+optionalQuery(tt.query), nil)
			rec := httptest.NewRecorder()

			h.ListFailedRuns(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want to contain %q", rec.Body.String(), tt.wantBody)
			}
			if store.lastLimit != tt.wantLimit {
				t.Errorf("limit passed to store = %d, want %d", store.lastLimit, tt.wantLimit)
			}
		})
	}
}

func optionalQuery(q string) string {
	if q == "" {
		return ""
	}
	return "?" + q
}
