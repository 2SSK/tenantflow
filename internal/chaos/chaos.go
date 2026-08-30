// Package chaos implements the worker's failure-injection switch.
//
// A Controller decides whether an activity call should fail on purpose, so
// the rest of the system (sagas, DLQ, tests) can be exercised under
// realistic failure without touching any activity code.
package chaos

import (
	"math/rand"
	"time"
)

// Controller decides whether an activity call should be failed deliberately.
//
// It is safe for concurrent use: worker threads call ShouldFail from many
// goroutines at once, and the only shared mutable state is rng, which is
// guarded by its own lock (a *rand.Rand is safe for concurrent use).
type Controller struct {
	rate float64
	// only is nil when chaos applies to every activity type.
	only map[string]bool
	// rng is injected so tests can seed a deterministic stream.
	rng *rand.Rand
}

// NewController builds a controller.
//
//	rate:      0..1 probability that an eligible activity call fails.
//	activities: activity type names to target; empty (or ["*"]) means all.
func NewController(rate float64, activities []string) *Controller {
	var only map[string]bool
	for _, a := range activities {
		if a == "*" {
			// "*" is sugar for "all activities" and means "no filter".
			only = nil
			break
		}
		if only == nil {
			only = make(map[string]bool)
		}
		only[a] = true
	}
	return &Controller{
		rate: rate,
		only: only,
		rng:  rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// ShouldFail reports whether a call to the given activity type should fail.
func (c *Controller) ShouldFail(activityType string) bool {
	if c.rate <= 0 {
		return false // chaos disabled
	}
	if c.only != nil && !c.only[activityType] {
		return false // this activity is not a target
	}
	// rand.Float64() returns a value in [0, 1), so "< rate" is true with
	// probability rate, and rate=1 fails every eligible call.
	return c.rng.Float64() < c.rate
}
