package metrics

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// gatherText renders the registry's current state as exposition text so a
// test can assert on exact series.
func gatherText(t *testing.T, r *Registry) string {
	t.Helper()
	mfs, err := r.prom.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var sb strings.Builder
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			str := &strings.Builder{}
			for _, p := range m.GetLabel() {
				str.WriteString(p.GetName() + "=" + p.GetValue() + " ")
			}
			switch mf.GetType().String() {
			case "COUNTER":
				sb.WriteString(mf.GetName() + "{" + str.String() + "} " + fmt.Sprintf("%g", m.GetCounter().GetValue()) + "\n")
			case "GAUGE":
				sb.WriteString(mf.GetName() + "{" + str.String() + "} " + fmt.Sprintf("%g", m.GetGauge().GetValue()) + "\n")
			case "HISTOGRAM":
				sb.WriteString(mf.GetName() + "{" + str.String() + "} sum=" + fmt.Sprintf("%g", m.GetHistogram().GetSampleSum()) + "\n")
			}
		}
	}
	return sb.String()
}

func TestTemporalAdapterCounterWithTags(t *testing.T) {
	r := New()
	h := NewTemporalHandler(r).WithTags(map[string]string{
		"namespace":     "default",
		"workflow_type": "ProvisionTenantWorkflow",
		"task_queue":    "tenantflow-provision",
	})

	h.Counter("temporal_workflow_completed").Inc(1)
	h.Counter("temporal_workflow_completed").Inc(3)

	text := gatherText(t, r)
	if !strings.Contains(text, "temporal_workflow_completed{namespace=default task_queue=tenantflow-provision workflow_type=ProvisionTenantWorkflow } 4") {
		t.Fatalf("counter with tags not gathered as expected:\n%s", text)
	}
}

func TestTemporalAdapterGaugeAndTimer(t *testing.T) {
	r := New()
	h := NewTemporalHandler(r)

	h.Gauge("temporal_num_pollers").Update(2)

	h.Timer("temporal_workflow_endtoend_latency").Record(1500 * time.Millisecond)
	h.Timer("temporal_workflow_endtoend_latency").Record(2 * time.Second)

	text := gatherText(t, r)
	if !strings.Contains(text, "temporal_num_pollers{} 2") {
		t.Fatalf("gauge not gathered as expected:\n%s", text)
	}
	// The timer records landed as a histogram whose sum is 3.5 seconds.
	if !strings.Contains(text, "temporal_workflow_endtoend_latency{} sum=3.5") {
		t.Fatalf("timer histogram not gathered as expected:\n%s", text)
	}
}

func TestWithTagsPreservesOldTagsAndOverwrites(t *testing.T) {
	r := New()
	h := NewTemporalHandler(r).WithTags(map[string]string{"namespace": "default", "workflow_type": "A"})
	merged := h.WithTags(map[string]string{"workflow_type": "B", "task_queue": "q"})

	merged.Counter("x").Inc(1)

	text := gatherText(t, r)
	if !strings.Contains(text, "namespace=default task_queue=q workflow_type=B") {
		t.Fatalf("merged tags wrong:\n%s", text)
	}
	if strings.Contains(text, "workflow_type=A") {
		t.Fatalf("old workflow_type survived an overwrite:\n%s", text)
	}
}

func TestActivityExecutionsCounter(t *testing.T) {
	r := New()
	// The custom counter is registered independently of the adapter.
	r.ActivityExecutions.WithLabelValues("ProvisionTenant", "failed").Inc()
	r.ActivityExecutions.WithLabelValues("ProvisionTenant", "failed").Inc()
	r.ActivityExecutions.WithLabelValues("MarkTenantActive", "completed").Inc()

	mfs, err := r.prom.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var found bool
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			labels := make(map[string]string)
			for _, p := range m.GetLabel() {
				labels[p.GetName()] = p.GetValue()
			}
			if labels["activity"] == "ProvisionTenant" && labels["result"] == "failed" {
				if v := m.GetCounter().GetValue(); v != 2 {
					t.Fatalf("expected 2 failed attempts, got %v", v)
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("tenantflow_activity_executions_total{activity=ProvisionTenant,result=failed} missing")
	}
}
