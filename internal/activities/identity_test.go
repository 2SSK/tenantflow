package activities

import (
	"context"
	"errors"
	"testing"
)

// fakeIdentityProvider records every call so a test can prove the activity
// used the get-or-create path (probe first, create only when absent).
type fakeIdentityProvider struct {
	users       map[string]string // username -> userID
	createCalls int
	assignCalls int
	probeCalls  int
	createErr   error
}

func newFakeIdentityProvider() *fakeIdentityProvider {
	return &fakeIdentityProvider{users: map[string]string{}}
}

func (f *fakeIdentityProvider) CreateUser(_ context.Context, username, _, _, _, _ string) (string, error) {
	f.createCalls++
	if f.createErr != nil {
		return "", f.createErr
	}
	userID := "created-" + username
	f.users[username] = userID
	return userID, nil
}

func (f *fakeIdentityProvider) GetUserByUsername(_ context.Context, username string) (string, bool, error) {
	f.probeCalls++
	userID, ok := f.users[username]
	return userID, ok, nil
}

func (f *fakeIdentityProvider) DeleteUser(_ context.Context, _ string) error { return nil }

func (f *fakeIdentityProvider) AssignRole(_ context.Context, _, _ string) error {
	f.assignCalls++
	return nil
}

func TestProvisionTenantIdentityCreatesWhenAbsent(t *testing.T) {
	fake := newFakeIdentityProvider()
	a := NewIdentityActivities(fake)

	userID, err := a.ProvisionTenantIdentity(context.Background(), "t-create")
	if err != nil {
		t.Fatalf("ProvisionTenantIdentity: %v", err)
	}

	if userID != "created-t-create-admin" {
		t.Errorf("userID = %q, want the created user", userID)
	}
	if fake.createCalls != 1 {
		t.Errorf("CreateUser calls = %d, want 1", fake.createCalls)
	}
	if fake.assignCalls != 1 {
		t.Errorf("AssignRole calls = %d, want 1", fake.assignCalls)
	}
	if fake.probeCalls != 1 {
		t.Errorf("GetUserByUsername calls = %d, want 1", fake.probeCalls)
	}
}

// TestProvisionTenantIdentityReusesExisting simulates the Temporal retry
// window: attempt 1 created the user but crashed before reporting success.
// The retry must NOT call CreateUser again (Keycloak would 409) — it probes,
// finds the user, and converges by re-assigning the role.
func TestProvisionTenantIdentityReusesExisting(t *testing.T) {
	fake := newFakeIdentityProvider()
	// The user already exists, as if a previous attempt created it.
	fake.users["t-retry-admin"] = "existing-user-abc"
	a := NewIdentityActivities(fake)

	userID, err := a.ProvisionTenantIdentity(context.Background(), "t-retry")
	if err != nil {
		t.Fatalf("ProvisionTenantIdentity on retry: %v", err)
	}

	if userID != "existing-user-abc" {
		t.Errorf("userID = %q, want the pre-existing user", userID)
	}
	if fake.createCalls != 0 {
		t.Errorf("CreateUser calls = %d on retry, want 0 (must not 409)", fake.createCalls)
	}
	if fake.assignCalls != 1 {
		t.Errorf("AssignRole calls = %d, want 1 (role must still converge)", fake.assignCalls)
	}
}

func TestProvisionTenantIdentityPropagatesCreateError(t *testing.T) {
	fake := newFakeIdentityProvider()
	fake.createErr = errors.New("keycloak down")
	a := NewIdentityActivities(fake)

	if _, err := a.ProvisionTenantIdentity(context.Background(), "t-fail"); err == nil {
		t.Fatal("expected error when CreateUser fails, got nil")
	}
}
