// Package metrics · interceptor: counts worker activity executions.
//
// The interceptor sits OUTERMOST in the worker's interceptor chain, so it
// sees the final result of every activity attempt — including failures
// injected by the chaos interceptor further down the chain. Each attempt is
// counted as it happens (retries included), keyed by activity name and
// result, which is exactly the shape the fail-matrix dashboard needs.
package metrics

import (
	"context"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
)

// Interceptor is a Temporal worker interceptor that increments
// tenantflow_activity_executions_total around every activity execution.
type Interceptor struct {
	interceptor.WorkerInterceptorBase
	reg *Registry
}

// NewInterceptor builds an Interceptor from a registry. The registry must
// not be nil.
func NewInterceptor(reg *Registry) *Interceptor {
	return &Interceptor{reg: reg}
}

// InterceptActivity wraps every activity execution with counting.
func (i *Interceptor) InterceptActivity(ctx context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &metricsActivity{
		ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next},
		reg:                            i.reg,
	}
}

// metricsActivity wraps a single activity call.
type metricsActivity struct {
	interceptor.ActivityInboundInterceptorBase
	reg *Registry
}

// ExecuteActivity counts the attempt result around the real activity call.
func (a *metricsActivity) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (interface{}, error) {
	out, err := a.ActivityInboundInterceptorBase.ExecuteActivity(ctx, in)

	result := "completed"
	if err != nil {
		result = "failed"
	}
	a.reg.ActivityExecutions.
		WithLabelValues(activity.GetInfo(ctx).ActivityType.Name, result).
		Inc()

	return out, err
}
