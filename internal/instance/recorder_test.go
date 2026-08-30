package instance

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/2SSK/tenantflow/internal/model"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

// memRepo is an in-memory WorkflowInstanceRepository so the recorder's DLQ
// mechanics can be tested without a database.
type memRepo struct {
	mu   sync.Mutex
	rows []model.WorkflowInstance
	err  error // when set, every write fails (best-effort path)
}

func (m *memRepo) InsertRunning(ctx context.Context, in model.WorkflowInstance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	in.Status = "running"
	m.rows = append(m.rows, in)
	return nil
}

func (m *memRepo) MarkFailed(ctx context.Context, workflowID, runID, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	for i := range m.rows {
		if m.rows[i].WorkflowID == workflowID && m.rows[i].RunID == runID {
			m.rows[i].Status = "failed"
			msg := errMsg
			m.rows[i].Error = &msg
		}
	}
	return nil
}

func (m *memRepo) ListFailed(ctx context.Context, limit int) ([]model.WorkflowInstance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []model.WorkflowInstance
	for _, r := range m.rows {
		if r.Status == "failed" {
			out = append(out, r)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func info(id, runID, wfType string) activity.Info {
	return activity.Info{
		WorkflowExecution: workflow.Execution{ID: id, RunID: runID},
		WorkflowType:      &workflow.Type{Name: wfType},
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
}

func TestTenantFromWorkflowID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"provision-acme-01", "acme-01"},
		{"upgrade-t1", "t1"},
		{"migrate-a-b", "a-b"},
		{"backup-b2", "b2"},
		{"restore-r9", "r9"},
		{"delete-d7", "d7"},
		{"unknown-id", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := tenantFromWorkflowID(c.in); got != c.want {
			t.Errorf("tenantFromWorkflowID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRecorder_RunningThenFailed(t *testing.T) {
	repo := &memRepo{}
	r := NewRecorder(repo, testLogger())

	r.markRunning(info("provision-acme-01", "run-1", "ProvisionTenantWorkflow"))
	r.markFailed(info("provision-acme-01", "run-1", "ProvisionTenantWorkflow"), errors.New("boom: the activity exploded"))

	if len(repo.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(repo.rows))
	}
	row := repo.rows[0]
	if row.Status != "failed" || row.Error == nil || *row.Error != "boom: the activity exploded" {
		t.Errorf("row = %+v, want failed with the activity error message", row)
	}
	if row.TenantID != "acme-01" || row.WorkflowType != "ProvisionTenantWorkflow" {
		t.Errorf("row = %+v, want tenant acme-01 and provision workflow type", row)
	}
}

func TestRecorder_RunningIsIdempotent(t *testing.T) {
	repo := &memRepo{}
	r := NewRecorder(repo, testLogger())

	// One run executes many activities; every one calls markRunning. Only the
	// first may insert a row (the seen-set suppresses the rest; the unique
	// constraint covers the window after a worker restart).
	r.markRunning(info("provision-acme-01", "run-1", "ProvisionTenantWorkflow"))
	r.markRunning(info("provision-acme-01", "run-1", "ProvisionTenantWorkflow"))
	r.markRunning(info("provision-acme-01", "run-1", "ProvisionTenantWorkflow"))

	if len(repo.rows) != 1 {
		t.Fatalf("rows = %d, want 1 (duplicate inserts must be suppressed)", len(repo.rows))
	}
}

func TestRecorder_BestEffortOnRepoError(t *testing.T) {
	repo := &memRepo{err: errors.New("db unreachable")}
	r := NewRecorder(repo, testLogger())

	// Recording is observability, not the transaction: a repo failure must be
	// swallowed (logged) and never escape into the activity path.
	r.markRunning(info("provision-acme-01", "run-1", "ProvisionTenantWorkflow"))
	r.markFailed(info("provision-acme-01", "run-1", "ProvisionTenantWorkflow"), errors.New("boom"))

	if len(repo.rows) != 0 {
		t.Fatalf("rows = %d, want 0 when the repo is failing", len(repo.rows))
	}
}
