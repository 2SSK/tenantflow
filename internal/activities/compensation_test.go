package activities

import "testing"

func TestCompensationEvent(t *testing.T) {
	payload := compensationEvent("RollbackQuotas", "saga compensation")

	if payload["compensation"] != true {
		t.Errorf("compensation flag = %v, want true", payload["compensation"])
	}
	if payload["step"] != "RollbackQuotas" {
		t.Errorf("step = %v, want RollbackQuotas", payload["step"])
	}
	if payload["reason"] != "saga compensation" {
		t.Errorf("reason = %v, want saga compensation", payload["reason"])
	}
}
