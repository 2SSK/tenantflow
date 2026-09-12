//go:build integration

package activities

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/2SSK/tenantflow/internal/identity"
)

// iKeycloakURL mirrors the config defaults so the test runs against the same
// Keycloak the rest of the stack uses, or against a TENANTFLOW_KEYCLOAK_URL
// override when the suite is pointed elsewhere.
func iKeycloakURL() string {
	if u := os.Getenv("TENANTFLOW_KEYCLOAK_URL"); u != "" {
		return u
	}
	return "http://localhost:8081"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// TestProvisionTenantIdentityGetOrCreateLive proves the get-or-create fix
// against real Keycloak: calling the activity twice (as a Temporal retry
// would after a lost result) must converge to the SAME user, create only one
// user, and never hit Keycloak's 409 duplicate-user error.
func TestProvisionTenantIdentityGetOrCreateLive(t *testing.T) {
	ctx := context.Background()
	url := iKeycloakURL()

	// Unreachable Keycloak (e.g. CI without the container) is a skip, not a
	// failure: the identity gap is already covered by the fast unit test.
	probe := identity.NewKeycloakProvider(url, "master",
		envOr("TENANTFLOW_KEYCLOAK_ADMIN_USER", "admin"),
		envOr("TENANTFLOW_KEYCLOAK_ADMIN_PASS", "admin"))
	if _, _, err := probe.GetUserByUsername(ctx, "no-such-user-"+fmt.Sprint(time.Now().UnixNano())); err != nil {
		t.Skipf("Keycloak not reachable/configured: %v", err)
	}

	provider := identity.NewKeycloakProvider(url, "tenantflow", "admin", "admin")
	act := NewIdentityActivities(provider)

	tenantID := fmt.Sprintf("itid-retry-%d", time.Now().UnixNano())

	// Attempt 1: ordinary provisioning.
	userID1, err := act.ProvisionTenantIdentity(ctx, tenantID)
	if err != nil {
		t.Fatalf("first provision: %v", err)
	}
	t.Cleanup(func() { _ = act.DeleteTenantIdentity(ctx, userID1) })

	// Attempt 2: the Temporal retry. Worker "died" after attempt 1 reported
	// nowhere; the activity runs again with the user already present.
	userID2, err := act.ProvisionTenantIdentity(ctx, tenantID)
	if err != nil {
		t.Fatalf("retry provision (get-or-create): %v", err)
	}

	if userID2 != userID1 {
		t.Errorf("retry returned a different user (%q vs %q); duplicate user created", userID2, userID1)
	}

	// Only ONE user with that username may exist in Keycloak.
	foundID, found, err := provider.GetUserByUsername(ctx, tenantID+"-admin")
	if err != nil {
		t.Fatalf("probe final user: %v", err)
	}
	if !found {
		t.Fatal("provisioned user not found after both attempts")
	}
	if foundID != userID1 {
		t.Errorf("only user in Keycloak = %q, want %q", foundID, userID1)
	}

	// DeleteUser is the sticky end of the lifecycle: retrying it after a
	// completed delete must not error (404 is treated as success).
	if err := act.DeleteTenantIdentity(ctx, userID1); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := act.DeleteTenantIdentity(ctx, userID1); err != nil {
		t.Fatalf("delete retry after user gone: %v", err)
	}
}
