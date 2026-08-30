package activities

// compensationEvent builds the standard audit payload for a saga rollback
// step. Every compensation step that mutates anything must emit an audit
// event carrying this shape, so the compensation history view can classify
// rollbacks purely from the payload:
//
//	{"compensation": true, "step": "<activity name>", "reason": "<why>"}
func compensationEvent(step, reason string) map[string]any {
	return map[string]any{
		"compensation": true,
		"step":         step,
		"reason":       reason,
	}
}
