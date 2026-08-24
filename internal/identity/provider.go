package identity

import "context"

type IdentityProvider interface {
	CreateUser(ctx context.Context, username, email, password, firstName, lastName string) (string, error)

	DeleteUser(ctx context.Context, userID string) error

	AssignRole(ctx context.Context, userID, roleName string) error
}
