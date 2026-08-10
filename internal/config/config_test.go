package config

import "testing"

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
}
