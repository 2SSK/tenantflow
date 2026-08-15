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
	repo repository.TenantRepository
}

func NewDeprovisionActivities(repo repository.TenantRepository) *DeprovisionActivities {
	return &DeprovisionActivities{repo: repo}
}

func (a *DeprovisionActivities) MarkTenantDeleting(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant deleting", "tenantID", tenantID)

	return a.repo.UpdateTenantStatus(ctx, tenantID, model.TenantStatusDeleting)
}

func (a *DeprovisionActivities) DeprovisionTenant(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Deprovision tenant", "tenantID", tenantID)

	time.Sleep(2 * time.Second)

	return nil
}

func (a *DeprovisionActivities) MarkTenantDeleted(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant deleted", "tenantID", tenantID)

	if err := a.repo.UpdateTenantStatus(ctx, tenantID, model.TenantStatusDeleted); err != nil {
		return fmt.Errorf("mark tenant %s deleted: %w", tenantID, err)
	}

	return nil
}
