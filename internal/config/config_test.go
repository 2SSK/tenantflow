package config

import (
	"testing"
	"time"
)

// TestLoadInvalidPort proves we fail fast instead of running with a
// garbage port. We set the env var to something non-numeric and expect
// Load() to return an error.
func TestLoadInvalidPort(t *testing.T) {
	t.Setenv("TENANTFLOW_HTTP_PORT", "not-an-integer")

	if _, err := Load(); err == nil {
		t.Fatal("expected error for non-integer port, got nil")
	}
}

// TestLoadDefaults proves that with no env vars set, we get sane defaults.
func TestLoadDefaults(t *testing.T) {
	t.Setenv("TENANTFLOW_HTTP_PORT", "")
	t.Setenv("TENANTFLOW_ENV", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.HTTPPort != 9090 {
		t.Errorf("expected default port 9090, got %d", cfg.HTTPPort)
	}

	if cfg.Env != "development" {
		t.Errorf("expected default env 'development', got %q", cfg.Env)
	}

	if cfg.TemporalAddress != "localhost:7233" {
		t.Errorf("expected default temporal address localhost:7233, got %q", cfg.TemporalAddress)
	}

	if cfg.TemporalNamespace != "default" {
		t.Errorf("expected default temporal namespace 'default', got %q", cfg.TemporalNamespace)
	}
}

func TestLoadTemporalOverrides(t *testing.T) {
	t.Setenv("TENANTFLOW_TEMPORAL_ADDRESS", "temporal.example.com:7233")
	t.Setenv("TENANTFLOW_TEMPORAL_NAMESPACE", "prod")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.TemporalAddress != "temporal.example.com:7233" {
		t.Errorf("expected overridden address, got %q", cfg.TemporalAddress)
	}

	if cfg.TemporalNamespace != "prod" {
		t.Errorf("expected overridden namespace, got %q", cfg.TemporalNamespace)
	}
}

// TestLoadReconcileSweepIntervalDefault proves the sweep defaults to 10m and
// can be disabled with 0.
func TestLoadReconcileSweepIntervalDefault(t *testing.T) {
	t.Setenv("TENANTFLOW_RECONCILE_SWEEP_INTERVAL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.ReconcileSweepInterval != 10*time.Minute {
		t.Errorf("expected default interval 10m, got %v", cfg.ReconcileSweepInterval)
	}

	t.Setenv("TENANTFLOW_RECONCILE_SWEEP_INTERVAL", "0s")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("unexpected error with 0s: %v", err)
	}
	if cfg.ReconcileSweepInterval != 0 {
		t.Errorf("expected disabled sweep for 0s, got %v", cfg.ReconcileSweepInterval)
	}
}

// TestLoadReconcileSweepIntervalOverride proves a valid override parses and an
// invalid duration is a hard error (never silently defaulted).
func TestLoadReconcileSweepIntervalOverride(t *testing.T) {
	t.Setenv("TENANTFLOW_RECONCILE_SWEEP_INTERVAL", "1h30m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ReconcileSweepInterval != 90*time.Minute {
		t.Errorf("expected 90m, got %v", cfg.ReconcileSweepInterval)
	}

	t.Setenv("TENANTFLOW_RECONCILE_SWEEP_INTERVAL", "not-a-duration")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for invalid duration, got nil")
	}
}
