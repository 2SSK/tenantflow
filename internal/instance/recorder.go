// Package instance records durable mirrors of workflow executions — the
// workflow_instances table (the dead letter queue) — from a Temporal worker
// interceptor, without touching any workflow or activity code.
package instance

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/2SSK/tenantflow/internal/model"
	"github.com/2SSK/tenantflow/internal/repository"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
)

// recorderInterval bounds each best-effort persistence write so a stalled
// database can never block the worker's activity path for long.
const recorderTimeout = 5 * time.Second

// Recorder is a Temporal worker interceptor that:
//
//   - inserts a "running" row the first time any activity of a run executes;
//   - flips that row to "failed" whenever an activity fails (after the retry
//     budget is exhausted, the workflow itself fails — the DLQ entry).
//
// Recording is best-effort observability: a persistence error is logged and
// never propagated, because failing an activity because *recording* failed
// would be a cascade bug.
type Recorder struct {
	interceptor.WorkerInterceptorBase
	repo repository.WorkflowInstanceRepository
	log  *slog.Logger

	mu   sync.Mutex
	seen map[string]bool // workflowID/runID already inserted as running
}

// NewRecorder builds a Recorder. The repo may not be nil.
func NewRecorder(repo repository.WorkflowInstanceRepository, log *slog.Logger) *Recorder {
	return &Recorder{repo: repo, log: log, seen: make(map[string]bool)}
}

// InterceptActivity wraps every activity execution with recording logic.
func (r *Recorder) InterceptActivity(ctx context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &recorderActivity{
		ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next},
		recorder:                       r,
	}
}

// recorderActivity wraps a single activity call.
type recorderActivity struct {
	interceptor.ActivityInboundInterceptorBase
	recorder *Recorder
}

// ExecuteActivity records the run state around the real activity call.
func (a *recorderActivity) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (interface{}, error) {
	info := activity.GetInfo(ctx)
	a.recorder.markRunning(info)

	out, err := a.ActivityInboundInterceptorBase.ExecuteActivity(ctx, in)
	if err != nil {
		a.recorder.markFailed(info, err)
	}
	return out, err
}

// markRunning inserts the "running" row once per run. The in-memory seen-set
// avoids a DB write on every activity of every run; the unique constraint
// (and ON CONFLICT DO NOTHING) covers the window after a worker restart.
func (r *Recorder) markRunning(info activity.Info) {
	key := info.WorkflowExecution.ID + "/" + info.WorkflowExecution.RunID
	r.mu.Lock()
	if r.seen[key] {
		r.mu.Unlock()
		return
	}
	r.seen[key] = true
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), recorderTimeout)
	defer cancel()
	if err := r.repo.InsertRunning(ctx, model.WorkflowInstance{
		TenantID:     tenantFromWorkflowID(info.WorkflowExecution.ID),
		WorkflowType: workflowTypeName(info),
		WorkflowID:   info.WorkflowExecution.ID,
		RunID:        info.WorkflowExecution.RunID,
	}); err != nil {
		r.log.Error("instance recorder: insert running",
			"workflowID", info.WorkflowExecution.ID,
			"runID", info.WorkflowExecution.RunID,
			"error", err)
	}
}

// markFailed records the failure. This runs for every failed activity attempt
// (each retry goes through the interceptor again), so MarkFailed is an UPDATE
// keyed on the same row — the row ends up holding the final attempt's error.
func (r *Recorder) markFailed(info activity.Info, cause error) {
	ctx, cancel := context.WithTimeout(context.Background(), recorderTimeout)
	defer cancel()
	if err := r.repo.MarkFailed(ctx, info.WorkflowExecution.ID, info.WorkflowExecution.RunID, cause.Error()); err != nil {
		r.log.Error("instance recorder: mark failed",
			"workflowID", info.WorkflowExecution.ID,
			"runID", info.WorkflowExecution.RunID,
			"error", err)
	}
}

// workflowTypeName returns the workflow type that started the activity.
func workflowTypeName(info activity.Info) string {
	if info.WorkflowType != nil {
		return info.WorkflowType.Name
	}
	return ""
}

// idPrefixes maps the workflow ID shapes the control plane starts
// (e.g. "provision-acme-01") back to the tenant they belong to.
var idPrefixes = []string{"provision-", "upgrade-", "migrate-", "backup-", "restore-", "delete-"}

// tenantFromWorkflowID strips the known workflow-type prefix off a workflow
// ID to recover the tenant ID; returns "" when the ID matches no known shape.
func tenantFromWorkflowID(id string) string {
	for _, p := range idPrefixes {
		if rest, ok := strings.CutPrefix(id, p); ok {
			return rest
		}
	}
	return ""
}
