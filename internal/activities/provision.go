package activities

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/2SSK/tenantflow/internal/cloud"
	"github.com/2SSK/tenantflow/internal/model"
	"github.com/2SSK/tenantflow/internal/repository"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

const (
	CreateTenantRecordActivityName = "CreateTenantRecord"
	ProvisionTenantActivityName    = "ProvisionTenant"
	MarkTenantActiveActivityName   = "MarkTenantActive"
	MarkTenantFailedActivityName   = "MarkTenantFailed"
	DropTenantDatabaseActivityName = "DropTenantDatabase"
)

type ProvisionActivities struct {
	repo      repository.TenantRepository
	auditRepo repository.AuditRepository
	provider  cloud.CloudProvider
}

func NewProvisionActivities(repo repository.TenantRepository, auditRepo repository.AuditRepository, provider cloud.CloudProvider) *ProvisionActivities {
	return &ProvisionActivities{repo: repo, auditRepo: auditRepo, provider: provider}
}

func (a *ProvisionActivities) CreateTenantRecord(ctx context.Context, tenantID string, workflowID string, isolationMode string) error {
	activity.GetLogger(ctx).Info("Creating tenant record", "tenantID", tenantID)

	mode := model.IsolationMode(isolationMode)
	if mode == "" {
		mode = model.IsolationModeDedicated
	}

	tenant := &model.Tenant{
		TenantID:      tenantID,
		Status:        model.TenantStatusProvisioning,
		IsolationMode: mode,
		WorkflowID:    &workflowID,
	}

	if err := a.repo.CreateTenant(ctx, tenant); err != nil {
		return err
	}

	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:   tenantID,
		WorkflowID: &workflowID,
		EventType:  model.AuditEventTenantCreated,
		Actor:      "workflow",
		Payload:    map[string]any{"status": "provisioning", "isolationMode": mode},
	})
}

func (a *ProvisionActivities) ProvisionTenant(ctx context.Context, tenantID string, isolationMode string) error {
	activity.GetLogger(ctx).Info("Provision tenant", "tenantID", tenantID, "isolationMode", isolationMode)

	if strings.HasPrefix(tenantID, "fail-") {
		return fmt.Errorf("simulated provisioning failure for tenant %s", tenantID)
	}

	// Shared-schema tenants do NOT get their own database — they share the
	// platform database and write into shared_* tables scoped by tenant_id.
	if isolationMode != string(model.IsolationModeShared) {
		if err := a.provider.CreateDatabase(ctx, tenantID); err != nil {
			return fmt.Errorf("create database for tenant %s: %w", tenantID, err)
		}
	}

	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantProvisioned,
		Actor:     "workflow",
		Payload:   map[string]any{"infra": "simulated", "isolationMode": isolationMode},
	})
}

func (a *ProvisionActivities) MarkTenantActive(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant active", "tenantID", tenantID)

	// Active is entered from "provisioning" (fresh provision) or "failed"
	// (the retry endpoint restarts the same workflow on a failed tenant —
	// CreateTenantRecord is a no-op then, so the status is still "failed").
	// Crucially "deleting" is NOT a legal source: if a delete workflow won
	// the race, this CAS fails and the provision workflow surfaces in the DLQ
	// instead of clobbering the delete back to active.
	if err := a.repo.UpdateTenantStatusFrom(ctx, tenantID, model.TenantStatusActive,
		model.TenantStatusProvisioning, model.TenantStatusFailed); err != nil {
		if errors.Is(err, repository.ErrStatusConflict) {
			return temporal.NewNonRetryableApplicationError("mark tenant "+tenantID+" active", "StatusConflict", err)
		}
		return fmt.Errorf("mark tenant %s active: %w", tenantID, err)
	}

	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantActivated,
		Actor:     "workflow",
		Payload:   map[string]any{},
	})
}

func (a *ProvisionActivities) MarkTenantFailed(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant failed (saga compensation)", "tenantID", tenantID)

	// Saga compensation: from "provisioning" on a fresh failure, or from
	// "failed" when a retried provision fails again. If a delete won the race
	// meanwhile, the CAS fails loudly instead of resurrecting a tenant that
	// is being torn down.
	if err := a.repo.UpdateTenantStatusFrom(ctx, tenantID, model.TenantStatusFailed,
		model.TenantStatusProvisioning, model.TenantStatusFailed); err != nil {
		if errors.Is(err, repository.ErrStatusConflict) {
			return temporal.NewNonRetryableApplicationError("mark tenant "+tenantID+" failed", "StatusConflict", err)
		}
		return fmt.Errorf("mark tenant %s failed: %w", tenantID, err)
	}
	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantFailed,
		Actor:     "workflow",
		Payload:   map[string]any{"reason": "saga compensation"},
	})
}

func (a *ProvisionActivities) DropTenantDatabase(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Dropping tenant database (saga compensation)", "tenantID", tenantID)

	if err := a.provider.DropDatabase(ctx, tenantID); err != nil {
		return fmt.Errorf("drop database for tenant %s: %w", tenantID, err)
	}

	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantProvisionRolledBack,
		Actor:     "workflow",
		Payload:   compensationEvent("DropTenantDatabase", "saga compensation"),
	})
}
