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

type IsolationMode string

const (
	IsolationModeDedicated IsolationMode = "dedicated"
	IsolationModeShared    IsolationMode = "shared"
)

type Tenant struct {
	TenantID      string
	Status        TenantStatus
	IsolationMode IsolationMode
	WorkflowID    *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
