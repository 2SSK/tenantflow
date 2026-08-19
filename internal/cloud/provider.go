package cloud

import "context"

type CloudProvider interface {
	CreateDatabase(ctx context.Context, tenantID string) error
	DropDatabase(ctx context.Context, tenantID string) error
}
