package chaos

import (
	"context"
	"fmt"
	"log/slog"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
)

// Interceptor is a Temporal worker interceptor that injects failures into
// activity executions according to a Controller's policy.
//
// Register it in sdkworker.Options{Interceptors: ...}. When the controller's
// rate is 0 it passes everything through untouched.
type Interceptor struct {
	interceptor.WorkerInterceptorBase // embed the base so we only override what we need
	Controller                        *Controller
	Log                               *slog.Logger
}

// InterceptActivity is called by the SDK once per activity execution, with
// "next" being the rest of the interceptor chain (ultimately the activity fn).
func (i *Interceptor) InterceptActivity(ctx context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &activityInterceptor{
		ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next},
		ctrl:                           i.Controller,
		log:                            i.Log,
	}
}

// activityInterceptor wraps a single activity call.
type activityInterceptor struct {
	interceptor.ActivityInboundInterceptorBase // embeds chain continuation
	ctrl                                       *Controller
	log                                        *slog.Logger
}

// ExecuteActivity runs before the real activity. If the controller says the
// activity type should fail, we return an error and the activity never runs.
func (a *activityInterceptor) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (interface{}, error) {
	info := activity.GetInfo(ctx) // real-world context: type, workflow, run, attempt
	if a.ctrl.ShouldFail(info.ActivityType.Name) {
		a.log.Warn("chaos: injecting activity failure",
			"activityType", info.ActivityType.Name,
			"workflowID", info.WorkflowExecution.ID,
			"runID", info.WorkflowExecution.RunID,
			"attempt", info.Attempt,
		)
		return nil, fmt.Errorf("chaos injection: simulated failure of activity %q (attempt %d)",
			info.ActivityType.Name, info.Attempt)
	}
	// Pass through to the next interceptor in the chain (the real activity).
	return a.ActivityInboundInterceptorBase.ExecuteActivity(ctx, in)
}
