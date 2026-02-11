package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadRequiredFields(t *testing.T) {
	// Missing both required fields
	os.Unsetenv("LAMBDA_API_TOKEN")
	os.Unsetenv("PROVIDER_CLUSTER_NAME")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing LAMBDA_API_TOKEN")
	}

	t.Setenv("LAMBDA_API_TOKEN", "test-token")
	_, err = Load()
	if err == nil {
		t.Fatal("expected error for missing PROVIDER_CLUSTER_NAME")
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("LAMBDA_API_TOKEN", "test-token")
	t.Setenv("PROVIDER_CLUSTER_NAME", "test-cluster")

	// Clear optional vars
	os.Unsetenv("LAMBDA_API_BASE_URL")
	os.Unsetenv("LAMBDA_API_RPS")
	os.Unsetenv("LAMBDA_LAUNCH_MIN_INTERVAL_SECONDS")
	os.Unsetenv("INSTANCE_TYPE_CACHE_TTL")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIToken != "test-token" {
		t.Fatalf("unexpected token: %s", cfg.APIToken)
	}
	if cfg.ClusterName != "test-cluster" {
		t.Fatalf("unexpected cluster: %s", cfg.ClusterName)
	}
	if cfg.BaseURL != defaultBaseURL {
		t.Fatalf("unexpected base url: %s", cfg.BaseURL)
	}
	if cfg.RPS != defaultRPS {
		t.Fatalf("unexpected rps: %d", cfg.RPS)
	}
	if cfg.LaunchMinInterval != defaultLaunchMinInterval {
		t.Fatalf("unexpected launch interval: %v", cfg.LaunchMinInterval)
	}
	if cfg.InstanceTypeCacheTTL != defaultInstanceTypeCacheTTL {
		t.Fatalf("unexpected cache ttl: %v", cfg.InstanceTypeCacheTTL)
	}
}

func TestLoadCustomValues(t *testing.T) {
	t.Setenv("LAMBDA_API_TOKEN", "tok")
	t.Setenv("PROVIDER_CLUSTER_NAME", "c1")
	t.Setenv("LAMBDA_API_BASE_URL", "https://custom.example.com")
	t.Setenv("LAMBDA_API_RPS", "5")
	t.Setenv("LAMBDA_LAUNCH_MIN_INTERVAL_SECONDS", "10")
	t.Setenv("INSTANCE_TYPE_CACHE_TTL", "5m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BaseURL != "https://custom.example.com" {
		t.Fatalf("unexpected base url: %s", cfg.BaseURL)
	}
	if cfg.RPS != 5 {
		t.Fatalf("unexpected rps: %d", cfg.RPS)
	}
	if cfg.LaunchMinInterval != 10*time.Second {
		t.Fatalf("unexpected launch interval: %v", cfg.LaunchMinInterval)
	}
	if cfg.InstanceTypeCacheTTL != 5*time.Minute {
		t.Fatalf("unexpected cache ttl: %v", cfg.InstanceTypeCacheTTL)
	}
}

func TestLoadInvalidValues(t *testing.T) {
	t.Setenv("LAMBDA_API_TOKEN", "tok")
	t.Setenv("PROVIDER_CLUSTER_NAME", "c1")
	t.Setenv("LAMBDA_API_RPS", "not-a-number")
	t.Setenv("LAMBDA_LAUNCH_MIN_INTERVAL_SECONDS", "-1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Invalid values should fall back to defaults
	if cfg.RPS != defaultRPS {
		t.Fatalf("expected default rps for invalid value, got: %d", cfg.RPS)
	}
	if cfg.LaunchMinInterval != defaultLaunchMinInterval {
		t.Fatalf("expected default interval for negative value, got: %v", cfg.LaunchMinInterval)
	}
}
