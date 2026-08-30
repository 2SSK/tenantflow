package activities

import (
	"context"

	"github.com/2SSK/tenantflow/internal/model"
	"github.com/2SSK/tenantflow/internal/repository"
	"go.temporal.io/sdk/activity"
)

const (
	RestoreTenantAfterCancelActivityName = "RestoreTenantAfterCancel"
	MarkTenantDeleteFailedActivityName   = "MarkTenantDeleteFailed"
)

// CancelDeleteActivities holds the steps only needed by the soft-delete saga's
// grace-period paths. The actual teardown steps (MarkTenantDeleting,
// DeprovisionTenant, MarkTenantDeleted) live in DeprovisionActivities and are
// reused by name — Temporal lets any workflow call any registered activity.
type CancelDeleteActivities struct {
	repo      repository.TenantRepository
	auditRepo repository.AuditRepository
}

func NewCancelDeleteActivities(repo repository.TenantRepository, auditRepo repository.AuditRepository) *CancelDeleteActivities {
	return &CancelDeleteActivities{repo: repo, auditRepo: auditRepo}
}

// RestoreTenantAfterCancel is the compensation for the grace period: the
// operator changed their mind, so flip the tenant back from "deleting" to
// "active" and record it. Nothing was torn down during the grace period, so
// this is a trivial state change — the expensive teardown only happens after
// the timer expires.
func (a *CancelDeleteActivities) RestoreTenantAfterCancel(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Restoring tenant after cancelled deletion", "tenantID", tenantID)

	if err := a.repo.UpdateTenantStatus(ctx, tenantID, model.TenantStatusActive); err != nil {
		return err
	}

	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantDeleteCancelled,
		Actor:     "workflow",
		Payload:   map[string]any{"reason": "grace period cancelled"},
	})
}

// MarkTenantDeleteFailed is the saga's deterministic failure compensation: it
// records that the soft-delete did not complete on the timeline so operators
// can investigate why a tenant is stuck in "deleting".
func (a *CancelDeleteActivities) MarkTenantDeleteFailed(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant delete as failed", "tenantID", tenantID)
	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantDeleteFailed,
		Actor:     "workflow",
		Payload:   map[string]any{"reason": "saga failed"},
	})
}
