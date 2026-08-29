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
	ListTenants(ctx context.Context) ([]model.Tenant, error)
}

type AuditStore interface {
	ListEvents(ctx context.Context, tenantID string) ([]model.AuditEvent, error)
}

// BackupStore lists the control-plane records of tenant backups.
type BackupStore interface {
	ListBackups(ctx context.Context, tenantID string) ([]model.Backup, error)
}

// TenantHandler handles /api/v1/tenants endpoints.
type TenantHandler struct {
	temporal    WorkflowStarter
	store       TenantStore
	auditStore  AuditStore
	backupStore BackupStore
	log         *slog.Logger
}

// NewTenantHandler wires a TenantHandler with its dependencies.
func NewTenantHandler(tc WorkflowStarter, store TenantStore, auditStore AuditStore, backupStore BackupStore, log *slog.Logger) *TenantHandler {
	return &TenantHandler{
		temporal:    tc,
		store:       store,
		auditStore:  auditStore,
		backupStore: backupStore,
		log:         log,
	}
}

// CreateTenantRequest is the POST /api/v1/tenants body
type CreateTenantRequest struct {
	TenantID      string `json:"tenantID"`
	IsolationMode string `json:"isolationMode"`
}

// CreateTenantResponse is returned with HTTP 202 Accepted.
type CreateTenantResponse struct {
	TenantID   string `json:"tenantID"`
	WorkflowID string `json:"workflowID"`
	Status     string `json:"status"`
}

// TenantResponse is the DTO returned by GET /api/v1/tenants/{tenantID}
type TenantResponse struct {
	TenantID      string    `json:"tenantID"`
	Status        string    `json:"status"`
	IsolationMode string    `json:"isolationMode"`
	WorkflowID    string    `json:"workflowID,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// DeleteTenantResponse is returned with HTTP 202 Accepted.
type DeleteTenantResponse struct {
	TenantID   string `json:"tenantID"`
	WorkflowID string `json:"workflowID"`
	Status     string `json:"status"`
}

// UpgradeTenantResponse is returned with HTTP 202 Accepted.
type UpgradeTenantResponse struct {
	TenantID   string `json:"tenantID"`
	WorkflowID string `json:"workflowID"`
	Status     string `json:"status"`
}

// MigrateTenantResponse is returned with HTTP 202 Accepted.
type MigrateTenantResponse struct {
	TenantID   string `json:"tenantID"`
	WorkflowID string `json:"workflowID"`
	Status     string `json:"status"`
}

// BackupTenantResponse is returned with HTTP 202 Accepted.
type BackupTenantResponse struct {
	TenantID   string `json:"tenantID"`
	WorkflowID string `json:"workflowID"`
	Status     string `json:"status"`
}

// RestoreTenantRequest is the POST /api/v1/tenants/{tenantID}/restore body.
type RestoreTenantRequest struct {
	BackupID int64 `json:"backupID"`
}

// RestoreTenantResponse is returned with HTTP 202 Accepted.
type RestoreTenantResponse struct {
	TenantID   string `json:"tenantID"`
	BackupID   int64  `json:"backupID"`
	WorkflowID string `json:"workflowID"`
	Status     string `json:"status"`
}

// ListTenantsResponse is returned by GET /api/v1/tenants
type ListTenantsResponse struct {
	Tenants []TenantResponse `json:"tenants"`
}

// BackupResponse is the DTO returned by GET /api/v1/tenants/{tenantID}/backups
type BackupResponse struct {
	ID          int64      `json:"id"`
	TenantID    string     `json:"tenantID"`
	Filename    string     `json:"filename"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

// ListBackupsResponse is returned by GET /api/v1/tenants/{tenantID}/backups
type ListBackupsResponse struct {
	Backups []BackupResponse `json:"backups"`
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

	// Default to dedicated; validate that the value is one of the known modes.
	isolationMode := req.IsolationMode
	if isolationMode == "" {
		isolationMode = string(model.IsolationModeDedicated)
	}
	if isolationMode != string(model.IsolationModeDedicated) && isolationMode != string(model.IsolationModeShared) {
		writeError(w, http.StatusBadRequest, "isolationMode must be 'dedicated' or 'shared'")
		return
	}

	workflowID := "provision-" + req.TenantID

	run, err := h.temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
		ID:                                       workflowID,
		TaskQueue:                                tfworkflow.TaskQueue,
		WorkflowIDReusePolicy:                    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, tfworkflow.ProvisionTenantWorkflow, tfworkflow.ProvisionInput{
		TenantID:      req.TenantID,
		IsolationMode: isolationMode,
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
		TenantID:      t.TenantID,
		Status:        string(t.Status),
		IsolationMode: string(t.IsolationMode),
		CreatedAt:     t.CreatedAt,
		UpdatedAt:     t.UpdatedAt,
	}

	if t.WorkflowID != nil {
		resp.WorkflowID = *t.WorkflowID
	}

	return resp
}

// DeleteTenant handles DELETE /api/v1/tenants/{tenantID}
func (h *TenantHandler) DeleteTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantID")

	if _, err := h.store.GetTenant(r.Context(), tenantID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		h.log.Error("get tenant for delete", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}

	workflowID := "deprovision-" + tenantID

	run, err := h.temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
		ID:                                       workflowID,
		TaskQueue:                                tfworkflow.TaskQueue,
		WorkflowIDReusePolicy:                    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, tfworkflow.DeprovisionTenantWorkflow, tfworkflow.DeprovisionInput{
		TenantID: tenantID,
	})
	if err != nil {
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &already) {
			writeError(w, http.StatusConflict, "deletion already in progress for this tenant")
			return
		}
		h.log.Error("start deprovision workflow", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to start deprovisioning workflow")
		return
	}

	h.log.Info("deprovision workflow started", "tenantID", tenantID, "workflowID", run.GetID())
	writeJSON(w, http.StatusAccepted, DeleteTenantResponse{
		TenantID:   tenantID,
		WorkflowID: run.GetID(),
		Status:     string(model.TenantStatusDeleting),
	})
}

// UpgradeTenant handles POST /api/v1/tenants/{tenantID}/upgrade
func (h *TenantHandler) UpgradeTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantID")

	tenant, err := h.store.GetTenant(r.Context(), tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		h.log.Error("get tenant for upgrade", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}

	// Only active tenants can be upgraded.
	if tenant.Status != model.TenantStatusActive {
		writeError(w, http.StatusConflict, "tenant must be active to upgrade")
		return
	}

	workflowID := "upgrade-" + tenantID

	run, err := h.temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
		ID:                                       workflowID,
		TaskQueue:                                tfworkflow.TaskQueue,
		WorkflowIDReusePolicy:                    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, tfworkflow.UpgradeTenantWorkflow, tfworkflow.UpgradeInput{
		TenantID: tenantID,
	})
	if err != nil {
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &already) {
			writeError(w, http.StatusConflict, "upgrade already in progress for this tenant")
			return
		}
		h.log.Error("start upgrade workflow", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to start upgrade workflow")
		return
	}

	h.log.Info("upgrade workflow started", "tenantID", tenantID, "workflowID", run.GetID())
	writeJSON(w, http.StatusAccepted, UpgradeTenantResponse{
		TenantID:   tenantID,
		WorkflowID: run.GetID(),
		Status:     "upgrading",
	})
}

// MigrateTenant handles POST /api/v1/tenants/{tenantID}/migrate
func (h *TenantHandler) MigrateTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantID")

	tenant, err := h.store.GetTenant(r.Context(), tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		h.log.Error("get tenant for migrate", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}

	// Only active tenants can be migrated (the saga keeps them active).
	if tenant.Status != model.TenantStatusActive {
		writeError(w, http.StatusConflict, "tenant must be active to migrate")
		return
	}

	workflowID := "migrate-" + tenantID

	run, err := h.temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
		ID:                                       workflowID,
		TaskQueue:                                tfworkflow.TaskQueue,
		WorkflowIDReusePolicy:                    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, tfworkflow.MigrateTenantWorkflow, tfworkflow.MigrateInput{
		TenantID: tenantID,
	})
	if err != nil {
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &already) {
			writeError(w, http.StatusConflict, "migration already in progress for this tenant")
			return
		}
		h.log.Error("start migrate workflow", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to start migration workflow")
		return
	}

	h.log.Info("migrate workflow started", "tenantID", tenantID, "workflowID", run.GetID())
	writeJSON(w, http.StatusAccepted, MigrateTenantResponse{
		TenantID:   tenantID,
		WorkflowID: run.GetID(),
		Status:     "migrating",
	})
}

// BackupTenant handles POST /api/v1/tenants/{tenantID}/backup
func (h *TenantHandler) BackupTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantID")

	tenant, err := h.store.GetTenant(r.Context(), tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		h.log.Error("get tenant for backup", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}

	// Only active tenants can be backed up (a live, usable database).
	if tenant.Status != model.TenantStatusActive {
		writeError(w, http.StatusConflict, "tenant must be active to backup")
		return
	}

	workflowID := "backup-" + tenantID

	run, err := h.temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
		ID:                                       workflowID,
		TaskQueue:                                tfworkflow.TaskQueue,
		WorkflowIDReusePolicy:                    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, tfworkflow.BackupTenantWorkflow, tfworkflow.BackupInput{
		TenantID: tenantID,
	})
	if err != nil {
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &already) {
			writeError(w, http.StatusConflict, "backup already in progress for this tenant")
			return
		}
		h.log.Error("start backup workflow", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to start backup workflow")
		return
	}

	h.log.Info("backup workflow started", "tenantID", tenantID, "workflowID", run.GetID())
	writeJSON(w, http.StatusAccepted, BackupTenantResponse{
		TenantID:   tenantID,
		WorkflowID: run.GetID(),
		Status:     "backing-up",
	})
}

// RestoreTenant handles POST /api/v1/tenants/{tenantID}/restore.
func (h *TenantHandler) RestoreTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantID")

	var req RestoreTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.BackupID <= 0 {
		writeError(w, http.StatusBadRequest, "backupID is required")
		return
	}

	tenant, err := h.store.GetTenant(r.Context(), tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		h.log.Error("get tenant for restore", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}

	// Only active tenants can be restored (a live, usable database to overwrite).
	if tenant.Status != model.TenantStatusActive {
		writeError(w, http.StatusConflict, "tenant must be active to restore")
		return
	}

	workflowID := "restore-" + tenantID

	run, err := h.temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
		ID:                                       workflowID,
		TaskQueue:                                tfworkflow.TaskQueue,
		WorkflowIDReusePolicy:                    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, tfworkflow.RestoreTenantWorkflow, tfworkflow.RestoreInput{
		TenantID: tenantID,
		BackupID: req.BackupID,
	})
	if err != nil {
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &already) {
			writeError(w, http.StatusConflict, "restore already in progress for this tenant")
			return
		}
		h.log.Error("start restore workflow", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to start restore workflow")
		return
	}

	h.log.Info("restore workflow started", "tenantID", tenantID, "backupID", req.BackupID, "workflowID", run.GetID())
	writeJSON(w, http.StatusAccepted, RestoreTenantResponse{
		TenantID:   tenantID,
		BackupID:   req.BackupID,
		WorkflowID: run.GetID(),
		Status:     "restoring",
	})
}

func (h *TenantHandler) ListTenants(w http.ResponseWriter, r *http.Request) {
	tenants, err := h.store.ListTenants(r.Context())
	if err != nil {
		h.log.Error("list tenants", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list tenants")
		return
	}

	resp := ListTenantsResponse{Tenants: make([]TenantResponse, 0, len(tenants))}
	for _, t := range tenants {
		resp.Tenants = append(resp.Tenants, toTenantResponse(&t))
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *TenantHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantID")

	if _, err := h.store.GetTenant(r.Context(), tenantID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		h.log.Error("get tenant for events", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}

	events, err := h.auditStore.ListEvents(r.Context(), tenantID)
	if err != nil {
		h.log.Error("list events", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list events")
		return
	}

	if events == nil {
		events = []model.AuditEvent{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// ListBackups handles GET /api/v1/tenants/{tenantID}/backups
func (h *TenantHandler) ListBackups(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantID")

	if _, err := h.store.GetTenant(r.Context(), tenantID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		h.log.Error("get tenant for backups", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}

	backups, err := h.backupStore.ListBackups(r.Context(), tenantID)
	if err != nil {
		h.log.Error("list backups", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list backups")
		return
	}

	resp := ListBackupsResponse{Backups: make([]BackupResponse, 0, len(backups))}
	for _, b := range backups {
		resp.Backups = append(resp.Backups, BackupResponse{
			ID:          b.ID,
			TenantID:    b.TenantID,
			Filename:    b.Filename,
			Status:      string(b.Status),
			CreatedAt:   b.CreatedAt,
			CompletedAt: b.CompletedAt,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}
