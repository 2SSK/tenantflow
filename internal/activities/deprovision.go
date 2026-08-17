package activities

import (
	"context"
	"fmt"
	"time"

	"github.com/2SSK/tenantflow/internal/model"
	"github.com/2SSK/tenantflow/internal/repository"
	"go.temporal.io/sdk/activity"
)

const (
	MarkTenantDeletingActivityName = "MarkTenantDeleting"
	DeprovisionTenantActivityName  = "DeprovisionTenant"
	MarkTenantDeletedActivityName  = "MarkTenantDeleted"
)

type DeprovisionActivities struct {
	repo      repository.TenantRepository
	auditRepo repository.AuditRepository
}

func NewDeprovisionActivities(repo repository.TenantRepository, auditRepo repository.AuditRepository) *DeprovisionActivities {
	return &DeprovisionActivities{repo: repo, auditRepo: auditRepo}
}

func (a *DeprovisionActivities) MarkTenantDeleting(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant deleting", "tenantID", tenantID)

	if err := a.repo.UpdateTenantStatus(ctx, tenantID, model.TenantStatusDeleting); err != nil {
		return err
	}

	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantDeleting,
		Actor:     "workflow",
		Payload:   map[string]any{},
	})
}

func (a *DeprovisionActivities) DeprovisionTenant(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Deprovision tenant", "tenantID", tenantID)

	time.Sleep(2 * time.Second)

	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantDeprovisioned,
		Actor:     "workflow",
		Payload:   map[string]any{"infra": "simulated teardown"},
	})
}

func (a *DeprovisionActivities) MarkTenantDeleted(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant deleted", "tenantID", tenantID)

	if err := a.repo.UpdateTenantStatus(ctx, tenantID, model.TenantStatusDeleted); err != nil {
		return fmt.Errorf("mark tenant %s deleted: %w", tenantID, err)
	}

	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantDeleted,
		Actor:     "workflow",
		Payload:   map[string]any{},
	})
}
