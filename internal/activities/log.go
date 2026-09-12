package activities

import (
	"context"
	"log/slog"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/log"
)

// logFor returns the Temporal activity logger when ctx belongs to an actual
// activity execution.
//
// The Temporal SDK PANICS when asked for an activity logger outside an
// activity context, which is exactly what happens when unit/integration tests
// drive an activity method directly (there is no worker wrapping the call).
// A recover-based probe is the only reliable way to detect that case, so we
// fall back to the process-level logger instead of crashing the test. Keeping
// this in one place means every activity stays directly testable against real
// infrastructure while still logging through Temporal's context-bound logger
// in production.
func logFor(ctx context.Context) log.Logger {
	if l, ok := activityLogger(ctx); ok {
		return l
	}
	return slog.Default()
}

func activityLogger(ctx context.Context) (l log.Logger, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			l, ok = nil, false
		}
	}()
	return activity.GetLogger(ctx), true
}
