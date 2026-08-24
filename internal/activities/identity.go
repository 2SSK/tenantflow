package activities

import (
	"context"
	"fmt"

	"github.com/2SSK/tenantflow/internal/identity"
	"go.temporal.io/sdk/activity"
)

const (
	ProvisionTenantIdentityActivityName = "ProvisionTenantIdentity"
	DeleteTenantIdentityActivityName    = "DeleteTenantIdentity"
)

type IdentityActivities struct {
	provider identity.IdentityProvider
}

func NewIdentityActivities(provider identity.IdentityProvider) *IdentityActivities {
	return &IdentityActivities{
		provider: provider,
	}
}

func (a *IdentityActivities) ProvisionTenantIdentity(ctx context.Context, tenantID string) (string, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Provisioning tenant identity", "tenantID", tenantID)

	username := tenantID + "-admin"
	email := username + "@tenantflow.local"
	password := tenantID + "-pass"
	firstName := tenantID
	lastName := "Admin"

	userID, err := a.provider.CreateUser(ctx, username, email, password, firstName, lastName)
	if err != nil {
		return "", fmt.Errorf("create identity for tenant %s: %w", tenantID, err)
	}

	if err := a.provider.AssignRole(ctx, userID, "platform-operator"); err != nil {
		return "", fmt.Errorf("assign role for tenant %s: %w", tenantID, err)
	}

	logger.Info("Tenant identity provisioned", "tenantID", tenantID, "userID", userID)
	return userID, nil
}

func (a *IdentityActivities) DeleteTenantIdentity(ctx context.Context, userID string) error {
	logger := activity.GetLogger(ctx)
	logger.Info("Deleting tenant identity", "userID", userID)

	if err := a.provider.DeleteUser(ctx, userID); err != nil {
		return fmt.Errorf("delete identity %s: %w", userID, err)
	}

	return nil
}
