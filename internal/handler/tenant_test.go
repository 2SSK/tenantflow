package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	tfworkflow "github.com/2SSK/tenantflow/internal/workflow"
)

type stubWorkflowStarter struct {
	startedOptions client.StartWorkflowOptions
	startedArgs    []any
	err            error
}

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

func newTestTenantHandler(s *stubWorkflowStarter) *TenantHandler {
	return NewTenantHandler(s, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
}

func TestCreateTenatAccept(t *testing.T) {
	stub := &stubWorkflowStarter{}
	h := newTestTenantHandler(stub)

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
	h := newTestTenantHandler(&stubWorkflowStarter{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewBufferString(`{not json`))
	rec := httptest.NewRecorder()

	h.CreateTenant(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateTenatMissingTenantID(t *testing.T) {
	stub := &stubWorkflowStarter{}
	h := newTestTenantHandler(&stubWorkflowStarter{})

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

	h := newTestTenantHandler(stub)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewBufferString(`{"tenantID":"acme"}`))
	rec := httptest.NewRecorder()

	h.CreateTenant(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
}
