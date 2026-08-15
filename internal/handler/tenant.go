package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/2SSK/tenantflow/internal/model"
	"github.com/2SSK/tenantflow/internal/repository"
	tfworkflow "github.com/2SSK/tenantflow/internal/workflow"
)

type WorkflowStarter interface {
	ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow any, args ...any) (client.WorkflowRun, error)
}

type TenantStore interface {
	GetTenant(ctx context.Context, tenantID string) (*model.Tenant, error)
}

// TenantHandler handles /api/v1/tenants endpoints.
type TenantHandler struct {
	temporal WorkflowStarter
	store    TenantStore
	log      *slog.Logger
}

// NewTenantHandler wires a TenantHandler with its dependencies.
func NewTenantHandler(tc WorkflowStarter, store TenantStore, log *slog.Logger) *TenantHandler {
	return &TenantHandler{
		temporal: tc,
		store:    store,
		log:      log,
	}
}

// CreateTenantRequest is the POST /api/v1/tenants body
type CreateTenantRequest struct {
	TenantID string `json:"tenantID"`
}

// CreateTenantResponse is returned with HTTP 202 Accepted.
type CreateTenantResponse struct {
	TenantID   string `json:"tenantID"`
	WorkflowID string `json:"workflowID"`
	Status     string `json:"status"`
}

// TenantResponse is the DTO returned by GET /api/v1/tenants/{tenantID}
type TenantResponse struct {
	TenantID   string    `json:"tenantID"`
	Status     string    `json:"status"`
	WorkflowID string    `json:"workflowID,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func (h *TenantHandler) CreateTenant(w http.ResponseWriter, r *http.Request) {
	var req CreateTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.TenantID == "" {
		writeError(w, http.StatusBadRequest, "tenantID is required")
		return
	}

	workflowID := "provision-" + req.TenantID

	run, err := h.temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
		ID:                                       workflowID,
		TaskQueue:                                tfworkflow.TaskQueue,
		WorkflowIDReusePolicy:                    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, tfworkflow.ProvisionTenantWorkflow, tfworkflow.ProvisionInput{
		TenantID: req.TenantID,
	})
	if err != nil {
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &already) {
			writeError(w, http.StatusConflict, "provisioning already in progress for this tenant")
			return
		}
		h.log.Error("start provision workflow", "tenantID", req.TenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to start provisioning workflow")
		return
	}

	h.log.Info("provision workflow started", "tenantID", req.TenantID, "workflowID", run.GetID())
	writeJSON(w, http.StatusAccepted, CreateTenantResponse{
		TenantID:   req.TenantID,
		WorkflowID: run.GetID(),
		Status:     "provisioning",
	})
}

// GetTenant handles GET /api/v1/tenants/{tenantID}
func (h *TenantHandler) GetTenant(w http.ResponseWriter, r *http.Request) {
	// Pull the tenantID out of the URL path.
	tenantID := r.PathValue("tenantID")

	tenant, err := h.store.GetTenant(r.Context(), tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}

		h.log.Error("get tenant", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}

	writeJSON(w, http.StatusOK, toTenantResponse(tenant))
}

// toTenantResponse converts the storage model into the API response shape.
func toTenantResponse(t *model.Tenant) TenantResponse {
	resp := TenantResponse{
		TenantID:  t.TenantID,
		Status:    string(t.Status),
		CreatedAt: t.CreatedAt,
		UpdatedAt: t.UpdatedAt,
	}

	if t.WorkflowID != nil {
		resp.WorkflowID = *t.WorkflowID
	}

	return resp
}
