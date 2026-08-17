package activities

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/2SSK/tenantflow/internal/model"
	"github.com/2SSK/tenantflow/internal/repository"
	"go.temporal.io/sdk/activity"
)

const (
	CreateTenantRecordActivityName = "CreateTenantRecord"
	ProvisionTenantActivityName    = "ProvisionTenant"
	MarkTenantActiveActivityName   = "MarkTenantActive"
	MarkTenantFailedActivityName   = "MarkTenantFailed"
)

type ProvisionActivities struct {
	repo      repository.TenantRepository
	auditRepo repository.AuditRepository
}

func NewProvisionActivities(repo repository.TenantRepository, auditRepo repository.AuditRepository) *ProvisionActivities {
	return &ProvisionActivities{repo: repo, auditRepo: auditRepo}
}

func (a *ProvisionActivities) CreateTenantRecord(ctx context.Context, tenantID string, workflowID string) error {
	activity.GetLogger(ctx).Info("Creating tenant record", "tenantID", tenantID)

	tenant := &model.Tenant{
		TenantID:   tenantID,
		Status:     model.TenantStatusProvisioning,
		WorkflowID: &workflowID,
	}

	if err := a.repo.CreateTenant(ctx, tenant); err != nil {
		return err
	}

	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:   tenantID,
		WorkflowID: &workflowID,
		EventType:  model.AuditEventTenantCreated,
		Actor:      "workflow",
		Payload:    map[string]any{"status": "provisioning"},
	})
}

func (a *ProvisionActivities) ProvisionTenant(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Provision tenant", "tenantID", tenantID)

	if strings.HasPrefix(tenantID, "fail-") {
		return fmt.Errorf("simulated provisioning failure for tenant %s", tenantID)
	}

	time.Sleep(2 * time.Second)

	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantProvisioned,
		Actor:     "workflow",
		Payload:   map[string]any{"infra": "simulated"},
	})
}

func (a *ProvisionActivities) MarkTenantActive(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant active", "tenantID", tenantID)

	if err := a.repo.UpdateTenantStatus(ctx, tenantID, model.TenantStatusActive); err != nil {
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

	if err := a.repo.UpdateTenantStatus(ctx, tenantID, model.TenantStatusFailed); err != nil {
		return fmt.Errorf("mark tenant %s failed: %w", tenantID, err)
	}
	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantFailed,
		Actor:     "workflow",
		Payload:   map[string]any{"reason": "saga compensation"},
	})
}
