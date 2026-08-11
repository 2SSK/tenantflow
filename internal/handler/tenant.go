package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	tfworkflow "github.com/2SSK/tenantflow/internal/workflow"
)

type WorkflowStarter interface {
	ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow any, args ...any) (client.WorkflowRun, error)
}

// TenantHandler handles /api/v1/tenants endpoints.
type TenantHandler struct {
	temporal WorkflowStarter
	log      *slog.Logger
}

// NewTenantHandler wires a TenantHandler with its dependencies.
func NewTenantHandler(tc WorkflowStarter, log *slog.Logger) *TenantHandler {
	return &TenantHandler{
		temporal: tc,
		log:      log,
	}
}

// CreateTenantRequest is the POST /api/v1/tenants body.
type CreateTenantRequest struct {
	TenantID string `json:"tenantID"`
}

// CreateTenantResponse is returned with HTTP 202 Accepted.
type CreateTenantResponse struct {
	TenantID   string `json:"tenantID"`
	WorkflowID string `json:"workflowID"`
	Status     string `json:"status"`
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
