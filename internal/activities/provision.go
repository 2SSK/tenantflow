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
	CreateTenantRecordActivityName = "CreateTenantRecord"
	ProvisionTenantActivityName    = "ProvisionTenant"
	MarkTenantActiveActivityName   = "MarkTenantActive"
)

type ProvisionActivities struct {
	repo repository.TenantRepository
}

func NewProvisionActivities(repo repository.TenantRepository) *ProvisionActivities {
	return &ProvisionActivities{repo: repo}
}

func (a *ProvisionActivities) CreateTenantRecord(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Creating tenant record", "tenantID", tenantID)

	tenant := &model.Tenant{
		TenantID: tenantID,
		Status:   model.TenantStatusProvisioning,
	}

	return a.repo.CreateTenant(ctx, tenant)
}

func (a *ProvisionActivities) ProvisionTenant(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Provision tenant", "tenantID", tenantID)

	time.Sleep(2 * time.Second)

	return nil
}

func (a *ProvisionActivities) MarkTenantActive(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant active", "tenantID", tenantID)

	if err := a.repo.UpdateTenantStatus(ctx, tenantID, model.TenantStatusActive); err != nil {
		return fmt.Errorf("mark tenant %s active: %w", tenantID, err)
	}
	return nil
}
