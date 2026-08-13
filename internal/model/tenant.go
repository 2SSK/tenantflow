package model

import "time"

type TenantStatus string

const (
	TenantStatusPending      TenantStatus = "pending"
	TenantStatusProvisioning TenantStatus = "provisioning"
	TenantStatusActive       TenantStatus = "active"
	TenantStatusFailed       TenantStatus = "failed"
	TenantStatusDeleting     TenantStatus = "deleting"
	TenantStatusDeleted      TenantStatus = "deleted"
)

type Tenant struct {
	TenantID   string
	Status     TenantStatus
	WorkflowID *string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
