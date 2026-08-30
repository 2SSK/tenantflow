package activities

import (
	"context"
	"fmt"

	"github.com/2SSK/tenantflow/internal/billing"
	"github.com/2SSK/tenantflow/internal/model"
	"github.com/2SSK/tenantflow/internal/repository"
	"go.temporal.io/sdk/activity"
)

const (
	VerifyTenantActiveActivityName      = "VerifyTenantActive"
	RaiseQuotasActivityName             = "RaiseQuotas"
	EnableFeaturesActivityName          = "EnableFeatures"
	UpdateBillingActivityName           = "UpdateBilling"
	RollbackQuotasActivityName          = "RollbackQuotas"
	EnableFeaturesCompActivityName      = "DisableFeatures"
	UpdateBillingCompActivityName       = "RefundBilling"
	MarkTenantUpgradingActivityName     = "MarkTenantUpgrading"
	MarkTenantUpgradedActivityName      = "MarkTenantUpgraded"
	MarkTenantUpgradeFailedActivityName = "MarkTenantUpgradeFailed"
)

type UpgradeActivities struct {
	repo      repository.TenantRepository
	auditRepo repository.AuditRepository
	quota     billing.QuotaStore
}

func NewUpgradeActivities(repo repository.TenantRepository, auditRepo repository.AuditRepository, quota billing.QuotaStore) *UpgradeActivities {
	return &UpgradeActivities{
		repo:      repo,
		auditRepo: auditRepo,
		quota:     quota,
	}
}

func (a *UpgradeActivities) VerifyTenantActive(ctx context.Context, tenantID string) (billing.Quota, error) {
	activity.GetLogger(ctx).Info("Verifying tenant is active", "tenantID", tenantID)

	tenant, err := a.repo.GetTenant(ctx, tenantID)
	if err != nil {
		return billing.Quota{}, fmt.Errorf("verify tenant %s: %w", tenantID, err)
	}
	if tenant.Status != model.TenantStatusActive {
		return billing.Quota{}, fmt.Errorf("tenant %s is not active", tenantID)
	}

	return a.quota.Get(ctx, tenantID)
}

func (a *UpgradeActivities) RaiseQuotas(ctx context.Context, tenantID string, old billing.Quota) (billing.Quota, error) {
	activity.GetLogger(ctx).Info("Raising quotas", "tenantID", tenantID)

	newQuota := billing.Quota{
		MaxUsers:     old.MaxUsers * 2,
		MaxStorageGB: old.MaxStorageGB * 4,
		MaxSeats:     old.MaxSeats * 2,
	}

	if err := a.quota.Set(ctx, tenantID, newQuota); err != nil {
		return billing.Quota{}, err
	}

	return newQuota, nil
}

func (a *UpgradeActivities) EnableFeatures(ctx context.Context, tenantID string, _ billing.Quota) error {
	activity.GetLogger(ctx).Info("Enabling features", "tenantID", tenantID)

	// In a real system: call the feature-flag service to enable premium flags for the tenant

	return nil
}

func (a *UpgradeActivities) UpdateBilling(ctx context.Context, tenantID string, _ billing.Quota) error {
	activity.GetLogger(ctx).Info("Updating billing tier", "tenantID", tenantID)

	// In a real system: call the billing service to set the new tier and
	// compute the prorated charge

	return nil
}

func (a *UpgradeActivities) RollbackQuotas(ctx context.Context, tenantID string, old billing.Quota) error {
	activity.GetLogger(ctx).Info("Rolling back quotas (compensation)", "tenantID", tenantID)

	if err := a.quota.Set(ctx, tenantID, old); err != nil {
		return err
	}

	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantQuotaRolled,
		Actor:     "workflow",
		Payload:   compensationEvent("RollbackQuotas", "saga compensation"),
	})
}

func (a *UpgradeActivities) MarkTenantUpgrading(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant as upgrading", "tenantID", tenantID)

	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantUpgrading,
		Actor:     "workflow",
		Payload:   map[string]any{},
	})
}

func (a *UpgradeActivities) MarkTenantUpgraded(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant as upgraded", "tenantID", tenantID)

	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantUpgraded,
		Actor:     "workflow",
		Payload:   map[string]any{},
	})
}

func (a *UpgradeActivities) MarkTenantUpgradeFailed(ctx context.Context, tenantID string) error {
	activity.GetLogger(ctx).Info("Marking tenant upgrade as failed", "tenantID", tenantID)

	return a.auditRepo.WriteEvent(ctx, &model.AuditEvent{
		TenantID:  tenantID,
		EventType: model.AuditEventTenantUpgradeFailed,
		Actor:     "workflow",
		Payload:   map[string]any{"reason": "saga failed"},
	})
}
