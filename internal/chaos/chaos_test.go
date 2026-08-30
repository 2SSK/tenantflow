package chaos

import (
	"math/rand"
	"testing"
)

func TestShouldFail_DisabledWhenRateZero(t *testing.T) {
	c := NewController(0, nil)
	for _, name := range []string{"ProvisionTenant", "MigrateData"} {
		if c.ShouldFail(name) {
			t.Fatalf("rate 0 must never fail %q", name)
		}
	}
}

func TestShouldFail_RateOneFailsEligibleOnly(t *testing.T) {
	c := NewController(1, []string{"ProvisionTenant"})
	if !c.ShouldFail("ProvisionTenant") {
		t.Fatal("eligible activity must always fail at rate 1")
	}
	if c.ShouldFail("MigrateData") {
		t.Fatal("non-eligible activity must never fail")
	}
}

func TestShouldFail_StarTargetsAll(t *testing.T) {
	c := NewController(1, []string{"*"})
	if !c.ShouldFail("AnythingAtAll") {
		t.Fatal("* must target every activity")
	}
}

func TestShouldFail_RateIsApproximatelyRespected(t *testing.T) {
	// Deterministic RNG injected directly so this test is never flaky.
	c := &Controller{rate: 0.5, rng: rand.New(rand.NewSource(42))}
	const n = 10000
	failures := 0
	for i := 0; i < n; i++ {
		if c.ShouldFail("ProvisionTenant") {
			failures++
		}
	}
	if failures < n*4/10 || failures > n*6/10 {
		t.Fatalf("expected ~5000 failures at rate 0.5, got %d", failures)
	}
}
