package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all runtime settings for the API.
type Config struct {
	Env      string
	HTTPPort int
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

	if cfg.Env != "development" && cfg.Env != "production" {
		return Config{}, fmt.Errorf("TENANTFLOW_ENV must be development or production, got %q", cfg.Env)
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
