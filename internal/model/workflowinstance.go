package model

import "time"

// WorkflowInstance mirrors one row of the workflow_instances table: the
// control plane's durable record of a Temporal workflow execution. It is the
// dead letter queue — rows with Status "failed" are failed runs awaiting an
// operator decision (investigate, ignore, or retry).
type WorkflowInstance struct {
	ID           string
	TenantID     string // may be empty when the workflow ID carries no tenant
	WorkflowType string
	WorkflowID   string
	RunID        string
	Status       string // running | failed (success is not observed by the recorder)
	Error        *string
	StartedAt    time.Time
	FinishedAt   *time.Time
}
