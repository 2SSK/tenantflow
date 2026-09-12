package activities

import (
	"context"
	"errors"
	"fmt"

	"github.com/2SSK/tenantflow/internal/cloud"
	"github.com/2SSK/tenantflow/internal/model"
	"github.com/2SSK/tenantflow/internal/repository"
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
	provider  cloud.CloudProvider
}

func NewDeprovisionActivities(repo repository.TenantRepository, auditRepo repository.AuditRepository, provider cloud.CloudProvider) *DeprovisionActivities {
	return &DeprovisionActivities{repo: repo, auditRepo: auditRepo, provider: provider}
}

func (a *DeprovisionActivities) MarkTenantDeleting(ctx context.Context, tenantID string) error {
	logFor(ctx).Info("Marking tenant deleting", "tenantID", tenantID)

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
	logFor(ctx).Info("Deprovision tenant", "tenantID", tenantID)

	// Real teardown, not a simulation: drop the tenant's dedicated database
	// and owner role. Both provider operations are idempotent (IF EXISTS), so
	// shared-schema tenants — which never got a database — no-op cleanly, and
	// a resumed DLQ replay of an already-partially-torn tenant converges.
	// The load test caught the old stub: it slept and wrote an audit event but
	// never touched the infrastructure, so 100 deleted dedicated tenants left
	// their databases behind forever.
	if err := a.provider.DropDatabase(ctx, tenantID); err != nil {
		return fmt.Errorf("drop database for tenant %s: %w", tenantID, err)
	}
	if err := a.provider.DropTenantRole(ctx, tenantID); err != nil {
		return fmt.Errorf("drop role for tenant %s: %w", tenantID, err)
	}

	// Drops are durable side effects recorded in history; the identity (the
	// Keycloak user) lifecycle is handled by the delete saga's identity step.
	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantDeprovisioned,
		Actor:     "workflow",
		Payload:   map[string]any{"teardown": "database and owner role dropped"},
	})
}

func (a *DeprovisionActivities) MarkTenantDeleted(ctx context.Context, tenantID string) error {
	logFor(ctx).Info("Marking tenant deleted", "tenantID", tenantID)

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
