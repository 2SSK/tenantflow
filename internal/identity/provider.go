package identity

import "context"

type IdentityProvider interface {
	CreateUser(ctx context.Context, username, email, password, firstName, lastName string) (string, error)

	// GetUserByUsername finds a user by their exact username and returns the
	// user ID. The bool is false when no user matches. Activities use this to
	// implement get-or-create so a Temporal retry converges instead of
	// failing with 409.
	GetUserByUsername(ctx context.Context, username string) (string, bool, error)

	DeleteUser(ctx context.Context, userID string) error

	AssignRole(ctx context.Context, userID, roleName string) error
}
