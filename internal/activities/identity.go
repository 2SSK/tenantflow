package activities

import (
	"context"
	"fmt"

	"github.com/2SSK/tenantflow/internal/identity"
)

const (
	ProvisionTenantIdentityActivityName      = "ProvisionTenantIdentity"
	DeleteTenantIdentityActivityName         = "DeleteTenantIdentity"
	DeleteTenantIdentityByTenantActivityName = "DeleteTenantIdentityByTenant"
)

// tenantIdentityUsername is the single source of truth for the username a
// tenant's identity owns. Provision creates it; the delete saga derives it to
// tear the identity down (a separate workflow run cannot read provision's
// userID variable). Keep both sides on this rule or provisioning and
// teardown silently stop reaching the same user.
func tenantIdentityUsername(tenantID string) string {
	return tenantID + "-admin"
}

type IdentityActivities struct {
	provider identity.IdentityProvider
}

func NewIdentityActivities(provider identity.IdentityProvider) *IdentityActivities {
	return &IdentityActivities{
		provider: provider,
	}
}

func (a *IdentityActivities) ProvisionTenantIdentity(ctx context.Context, tenantID string) (string, error) {
	logger := logFor(ctx)
	logger.Info("Provisioning tenant identity", "tenantID", tenantID)

	username := tenantIdentityUsername(tenantID)
	email := username + "@tenantflow.local"
	password := tenantID + "-pass"
	firstName := tenantID
	lastName := "Admin"

	// Get-or-create: Keycloak's POST /users returns 409 when the username
	// already exists, so a retried activity (worker died after the create but
	// before reporting success) would previously fail and send the whole
	// provision saga into the DLQ until a human deleted the user by hand.
	// Probe first; reuse the existing user when present.
	userID, found, err := a.provider.GetUserByUsername(ctx, username)
	if err != nil {
		return "", fmt.Errorf("probe identity for tenant %s: %w", tenantID, err)
	}
	if !found {
		userID, err = a.provider.CreateUser(ctx, username, email, password, firstName, lastName)
		if err != nil {
			return "", fmt.Errorf("create identity for tenant %s: %w", tenantID, err)
		}
	} else {
		logger.Info("tenant identity already exists; reusing", "tenantID", tenantID, "userID", userID)
	}

	// Role mapping is a set operation in Keycloak, so re-assigning converges:
	// a pre-existing user (or a retry of this activity) gets the role without
	// erroring.
	if err := a.provider.AssignRole(ctx, userID, "platform-operator"); err != nil {
		return "", fmt.Errorf("assign role for tenant %s: %w", tenantID, err)
	}

	logger.Info("Tenant identity provisioned", "tenantID", tenantID, "userID", userID)
	return userID, nil
}

func (a *IdentityActivities) DeleteTenantIdentity(ctx context.Context, userID string) error {
	logger := logFor(ctx)
	logger.Info("Deleting tenant identity", "userID", userID)

	if err := a.provider.DeleteUser(ctx, userID); err != nil {
		return fmt.Errorf("delete identity %s: %w", userID, err)
	}

	return nil
}

// DeleteTenantIdentityByTenant tears down a tenant's identity when the delete
// saga only knows the tenantID (a separate workflow run; provision's userID
// variable is not visible here). Derives the username, probes, no-ops when
// the user is already gone. Used by DeleteTenantWorkflow.
func (a *IdentityActivities) DeleteTenantIdentityByTenant(ctx context.Context, tenantID string) error {
	logger := logFor(ctx)
	username := tenantIdentityUsername(tenantID)

	userID, found, err := a.provider.GetUserByUsername(ctx, username)
	if err != nil {
		return fmt.Errorf("probe identity for tenant %s: %w", tenantID, err)
	}
	if !found {
		logger.Info("no tenant identity to delete; skipping", "tenantID", tenantID)
		return nil
	}

	return a.DeleteTenantIdentity(ctx, userID)
}
