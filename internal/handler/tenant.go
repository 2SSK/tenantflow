package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
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
	// SignalWorkflow delivers a named signal to a running workflow — used by
	// the cancel-delete endpoint to interrupt the soft-delete grace period.
	SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg any) error
}

type TenantStore interface {
	GetTenant(ctx context.Context, tenantID string) (*model.Tenant, error)
	ListTenants(ctx context.Context) ([]model.Tenant, error)
}

type AuditStore interface {
	WriteEvent(ctx context.Context, event *model.AuditEvent) error
	ListEvents(ctx context.Context, tenantID string) ([]model.AuditEvent, error)
}

// BackupStore lists the control-plane records of tenant backups.
type BackupStore interface {
	ListBackups(ctx context.Context, tenantID string) ([]model.Backup, error)
}

// FailedRunStore lists the dead letter queue — durable workflow-instance
// mirrors of failed runs.
type FailedRunStore interface {
	ListFailed(ctx context.Context, limit int) ([]model.WorkflowInstance, error)
}

// TenantHandler handles /api/v1/tenants endpoints.
type TenantHandler struct {
	temporal    WorkflowStarter
	store       TenantStore
	auditStore  AuditStore
	backupStore BackupStore
	failedRuns  FailedRunStore
	log         *slog.Logger
}

// NewTenantHandler wires a TenantHandler with its dependencies.
func NewTenantHandler(tc WorkflowStarter, store TenantStore, auditStore AuditStore, backupStore BackupStore, failedRuns FailedRunStore, log *slog.Logger) *TenantHandler {
	return &TenantHandler{
		temporal:    tc,
		store:       store,
		auditStore:  auditStore,
		backupStore: backupStore,
		failedRuns:  failedRuns,
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

// defaultDeleteGracePeriod is the soft-delete window an operator may cancel
// within. The workflow receives this via DeleteInput so tests and tooling can
// pass shorter durations.
const defaultDeleteGracePeriod = 30 * 24 * time.Hour

// DeleteTenantResponse is returned with HTTP 202 Accepted.
// GracePeriodDays tells the operator how long they have to cancel the deletion.
type DeleteTenantResponse struct {
	TenantID        string `json:"tenantID"`
	WorkflowID      string `json:"workflowID"`
	Status          string `json:"status"`
	GracePeriodDays int    `json:"gracePeriodDays"`
}

// CancelTenantDeleteResponse is returned with HTTP 202 Accepted when a
// cancel-delete signal has been accepted.
type CancelTenantDeleteResponse struct {
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
//
// This starts a soft-delete workflow: the tenant is marked "deleting" and
// stays that way for the 30-day grace period before teardown. The operator
// can cancel within that window via POST /api/v1/tenants/{tenantID}/cancel-delete.
func (h *TenantHandler) DeleteTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantID")

	tenant, err := h.store.GetTenant(r.Context(), tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		h.log.Error("get tenant for delete", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}

	// A tenant already being deleted (or already deleted) must not start a
	// second grace period. A "failed" tenant is still deletable — deletes can
	// be used as a cleanup operation.
	if tenant.Status == model.TenantStatusDeleting {
		writeError(w, http.StatusConflict, "deletion already in progress for this tenant")
		return
	}
	if tenant.Status == model.TenantStatusDeleted {
		writeError(w, http.StatusConflict, "tenant already deleted")
		return
	}

	workflowID := "delete-" + tenantID

	run, err := h.temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
		ID:                                       workflowID,
		TaskQueue:                                tfworkflow.TaskQueue,
		WorkflowIDReusePolicy:                    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, tfworkflow.DeleteTenantWorkflow, tfworkflow.DeleteInput{
		TenantID:    tenantID,
		GracePeriod: defaultDeleteGracePeriod,
	})
	if err != nil {
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &already) {
			writeError(w, http.StatusConflict, "deletion already in progress for this tenant")
			return
		}
		h.log.Error("start delete workflow", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to start delete workflow")
		return
	}

	h.log.Info("delete workflow started", "tenantID", tenantID, "workflowID", run.GetID())
	writeJSON(w, http.StatusAccepted, DeleteTenantResponse{
		TenantID:        tenantID,
		WorkflowID:      run.GetID(),
		Status:          string(model.TenantStatusDeleting),
		GracePeriodDays: int(defaultDeleteGracePeriod.Hours() / 24),
	})
}

// CancelTenantDelete handles POST /api/v1/tenants/{tenantID}/cancel-delete
//
// It signals the running DeleteTenantWorkflow to abort the grace period and
// restore the tenant to active. The signal is delivered asynchronously by
// Temporal, so we return 202 as soon as it is accepted, not when the restore
// actually happens.
func (h *TenantHandler) CancelTenantDelete(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantID")

	tenant, err := h.store.GetTenant(r.Context(), tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		h.log.Error("get tenant for cancel delete", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}

	// Only a tenant inside the grace period can be cancelled. If it is already
	// gone or never was deleting, there is nothing to cancel.
	if tenant.Status != model.TenantStatusDeleting {
		writeError(w, http.StatusConflict, "tenant is not being deleted")
		return
	}

	workflowID := "delete-" + tenantID

	// An empty runID means "the running execution of this workflow ID" — we
	// do not care which run it is, only that one exists for this tenant.
	if err := h.temporal.SignalWorkflow(r.Context(), workflowID, "", tfworkflow.CancelDeleteSignalName, nil); err != nil {
		// The workflow may have just completed (timer fired, teardown done) or
		// failed between our get and the signal — an unavoidable race.
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			writeError(w, http.StatusConflict, "delete workflow is not running")
			return
		}
		h.log.Error("signal cancel delete", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to signal delete workflow")
		return
	}

	h.log.Info("cancel delete signalled", "tenantID", tenantID, "workflowID", workflowID)
	writeJSON(w, http.StatusAccepted, CancelTenantDeleteResponse{
		TenantID:   tenantID,
		WorkflowID: workflowID,
		Status:     "cancelling",
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

// FailedRunResponse is one entry of the dead letter queue as served to clients.
type FailedRunResponse struct {
	TenantID     string     `json:"tenantID"`
	WorkflowType string     `json:"workflowType"`
	WorkflowID   string     `json:"workflowID"`
	RunID        string     `json:"runID"`
	ErrorMessage string     `json:"errorMessage,omitempty"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
}

// ListFailedRunsResponse is returned by GET /api/v1/failed-runs.
type ListFailedRunsResponse struct {
	Runs []FailedRunResponse `json:"runs"`
}

// ListFailedRuns handles GET /api/v1/failed-runs — the dead letter queue:
// durable workflow-instance mirrors of failed runs, most recent first.
func (h *TenantHandler) ListFailedRuns(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	instances, err := h.failedRuns.ListFailed(r.Context(), limit)
	if err != nil {
		h.log.Error("list failed runs", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list failed runs")
		return
	}

	runs := make([]FailedRunResponse, 0, len(instances))
	for _, inst := range instances {
		errMsg := ""
		if inst.Error != nil {
			errMsg = *inst.Error
		}
		runs = append(runs, FailedRunResponse{
			TenantID:     inst.TenantID,
			WorkflowType: inst.WorkflowType,
			WorkflowID:   inst.WorkflowID,
			RunID:        inst.RunID,
			ErrorMessage: errMsg,
			StartedAt:    inst.StartedAt,
			FinishedAt:   inst.FinishedAt,
		})
	}

	writeJSON(w, http.StatusOK, ListFailedRunsResponse{Runs: runs})
}

// RetryTenantResponse is returned with HTTP 202 Accepted when a failed tenant
// has been re-submitted for provisioning (a manual DLQ replay).
type RetryTenantResponse struct {
	TenantID   string `json:"tenantID"`
	WorkflowID string `json:"workflowID"`
	RunID      string `json:"runID"`
	Message    string `json:"message"`
}

// RetryTenant handles POST /api/v1/tenants/{tenantID}/retry.
//
// It restarts the provisioning workflow for a tenant left in the 'failed'
// state, reusing the same workflow ID (allowed once the previous run is
// closed, which a failed run is). The manual replay is recorded in the audit
// stream so the recovery is traceable.
func (h *TenantHandler) RetryTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantID")

	tenant, err := h.store.GetTenant(r.Context(), tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		h.log.Error("load tenant for retry", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load tenant")
		return
	}

	// The DLQ manual-replay story only makes sense for tenants the saga
	// actually left failed; everything else has a different recovery path.
	if tenant.Status != model.TenantStatusFailed {
		writeError(w, http.StatusConflict, "only failed tenants can be retried")
		return
	}

	workflowID := "provision-" + tenantID
	run, err := h.temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
		ID:                                       workflowID,
		TaskQueue:                                tfworkflow.TaskQueue,
		WorkflowIDReusePolicy:                    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, tfworkflow.ProvisionTenantWorkflow, tfworkflow.ProvisionInput{
		TenantID:      tenantID,
		IsolationMode: string(tenant.IsolationMode),
	})
	if err != nil {
		h.log.Error("retry provision workflow", "tenantID", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to restart provisioning workflow")
		return
	}

	// Record the manual replay. The actor is the fixed role that may call
	// this endpoint (actor identity is not yet plumbed into the request).
	if err := h.auditStore.WriteEvent(r.Context(), &model.AuditEvent{
		TenantID:   tenantID,
		WorkflowID: &workflowID,
		EventType:  model.AuditEventTenantReprovisionReq,
		Actor:      "platform-admin",
		Payload:    map[string]any{"runID": run.GetRunID()},
	}); err != nil {
		h.log.Error("record retry audit", "tenantID", tenantID, "error", err)
	}

	h.log.Info("provision workflow retried", "tenantID", tenantID,
		"workflowID", run.GetID(), "runID", run.GetRunID())
	writeJSON(w, http.StatusAccepted, RetryTenantResponse{
		TenantID:   tenantID,
		WorkflowID: run.GetID(),
		RunID:      run.GetRunID(),
		Message:    "provisioning workflow restarted",
	})
}
