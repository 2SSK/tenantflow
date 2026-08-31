package activities

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/2SSK/tenantflow/internal/model"
	"github.com/2SSK/tenantflow/internal/repository"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
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

	// Deletion may only ENTER "deleting" from a stable state. If a concurrent
	// lifecycle workflow (e.g. provision) won the race, the CAS fails. The
	// conflict is non-retryable: the status will not change back on its own,
	// so retrying would only pump three identical failures into the DLQ.
	if err := a.repo.UpdateTenantStatusFrom(ctx, tenantID, model.TenantStatusDeleting,
		model.TenantStatusActive, model.TenantStatusFailed); err != nil {
		if errors.Is(err, repository.ErrStatusConflict) {
			return temporal.NewNonRetryableApplicationError("mark tenant "+tenantID+" deleting", "StatusConflict", err)
		}
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

	// Teardown may only finish from "deleting". A fresh DELETE on an already
	// deleted tenant fails here instead of silently succeeding — the DLQ
	// surfaces the anomaly for the operator.
	if err := a.repo.UpdateTenantStatusFrom(ctx, tenantID, model.TenantStatusDeleted, model.TenantStatusDeleting); err != nil {
		if errors.Is(err, repository.ErrStatusConflict) {
			return temporal.NewNonRetryableApplicationError("mark tenant "+tenantID+" deleted", "StatusConflict", err)
		}
		return fmt.Errorf("mark tenant %s deleted: %w", tenantID, err)
	}

	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantDeleted,
		Actor:     "workflow",
		Payload:   map[string]any{},
	})
}
