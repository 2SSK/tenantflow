package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime settings for the API.
type Config struct {
	Env               string
	HTTPPort          int
	TemporalAddress   string
	TemporalNamespace string
	DatabaseURL       string

	KeycloakURL         string
	KeycloakRealm       string
	KeycloakClientID    string
	KeycloakSecret      string
	KeycloakRedirectURL string

	// Keycloak admin credentials (for user management, not API auth)
	KeycloakAdminUser string
	KeycloakAdminPass string

	// WorkerMetricsAddr is the listen address for the worker's Prometheus
	// scrape endpoint (":9091" by default).
	WorkerMetricsAddr string

	// ReconcileSweepInterval is how often the scheduled reconcile sweep (Phase
	// 14) runs. 0 disables the sweep entirely (the worker boots without
	// starting the loop). Default is 10m.
	ReconcileSweepInterval time.Duration

	// Chaos controls the worker's failure-injection switch (Phase 8).
	Chaos ChaosConfig
}

// ChaosConfig configures deliberate activity failure injection.
// With Rate 0 the worker never injects failures and the interceptor is a no-op.
type ChaosConfig struct {
	// Rate is the probability (0..1) that an eligible activity call fails.
	Rate float64
	// Activities restricts chaos to these activity type names.
	// Empty means chaos applies to every activity.
	Activities []string
}

// Load reads configuration from the process environment.
// Missing variables fall back to defaults; invalid values return an error.
func Load() (Config, error) {
	cfg := Config{
		Env: getEnv("TENANTFLOW_ENV", "development"),
	}

	port, err := getEnvInt("TENANTFLOW_HTTP_PORT", 9090)
	if err != nil {
		return Config{}, err
	}
	cfg.HTTPPort = port

	cfg.TemporalAddress = getEnv("TENANTFLOW_TEMPORAL_ADDRESS", "localhost:7233")
	cfg.TemporalNamespace = getEnv("TENANTFLOW_TEMPORAL_NAMESPACE", "default")

	cfg.DatabaseURL = getEnv("TENANTFLOW_DATABASE_URL", "postgres://tenantflow:tenantflow@localhost:5432/tenantflow?sslmode=disable")

	cfg.KeycloakURL = getEnv("TENANTFLOW_KEYCLOAK_URL", "http://localhost:8081")
	cfg.KeycloakRealm = getEnv("TENANTFLOW_KEYCLOAK_REALM", "tenantflow")
	cfg.KeycloakClientID = getEnv("TENANTFLOW_KEYCLOAK_CLIENT_ID", "tenantflow-api")
	cfg.KeycloakSecret = getEnv("TENANTFLOW_KEYCLOAK_SECRET", "api-secret-123")
	cfg.KeycloakRedirectURL = getEnv("TENANTFLOW_KEYCLOAK_REDIRECT_URL", "http://localhost:3000/callback")
	cfg.KeycloakAdminUser = getEnv("TENANTFLOW_KEYCLOAK_ADMIN_USER", "admin")
	cfg.KeycloakAdminPass = getEnv("TENANTFLOW_KEYCLOAK_ADMIN_PASS", "admin")

	// Worker-side Prometheus scrape endpoint. The API reuses HTTPPort.
	cfg.WorkerMetricsAddr = getEnv("TENANTFLOW_WORKER_METRICS_ADDR", ":9091")

	interval, err := getEnvDuration("TENANTFLOW_RECONCILE_SWEEP_INTERVAL", 10*time.Minute)
	if err != nil {
		return Config{}, err
	}
	cfg.ReconcileSweepInterval = interval

	if cfg.Env != "development" && cfg.Env != "production" {
		return Config{}, fmt.Errorf("TENANTFLOW_ENV must be development or production, got %q", cfg.Env)
	}

	chaosRate := 0.0
	if s := os.Getenv("TENANTFLOW_CHAOS_RATE"); s != "" {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return Config{}, fmt.Errorf("TENANTFLOW_CHAOS_RATE must be a number: %w", err)
		}
		if f < 0 || f > 1 {
			return Config{}, fmt.Errorf("TENANTFLOW_CHAOS_RATE must be between 0 and 1, got %v", f)
		}
		chaosRate = f
	}
	cfg.Chaos = ChaosConfig{
		Rate:       chaosRate,
		Activities: splitList(os.Getenv("TENANTFLOW_CHAOS_ACTIVITIES")),
	}

	return cfg, nil
}

// getEnv returns the value of the environment variable, or fallback if unset.
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// splitList splits a comma-separated string into trimmed, non-empty entries.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// getEnvInt is getEnv for integers.
func getEnvInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid value for %s: %q (must be an integer)", key, v)
	}
	return n, nil
}

// getEnvDuration is getEnv for durations ("30s", "10m"). An empty var falls
// back silently; a non-empty invalid value is a hard error so a typo in a
// duration never disables or accelerates a loop by accident.
func getEnvDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid value for %s: %q (must be a duration like 5m or 1h)", key, v)
	}
	return d, nil
}
