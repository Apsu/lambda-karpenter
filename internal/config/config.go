package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	defaultBaseURL              = "https://cloud.lambda.ai"
	defaultRPS                  = 1
	defaultLaunchMinInterval    = 5 * time.Second
	defaultInstanceTypeCacheTTL = 10 * time.Minute
	requiredClusterNameEnv      = "PROVIDER_CLUSTER_NAME"
	requiredTokenEnv            = "LAMBDA_API_TOKEN"
	baseURLEnv                  = "LAMBDA_API_BASE_URL"
	rpsEnv                      = "LAMBDA_API_RPS"
	launchMinIntervalSecondsEnv = "LAMBDA_LAUNCH_MIN_INTERVAL_SECONDS"
	instanceTypeCacheTTLEnv     = "INSTANCE_TYPE_CACHE_TTL"
)

// Config defines provider configuration loaded from environment variables.
type Config struct {
	APIToken             string
	BaseURL              string
	ClusterName          string
	RPS                  int
	LaunchMinInterval    time.Duration
	InstanceTypeCacheTTL time.Duration
}

func Load() (Config, error) {
	cfg := Config{}

	cfg.APIToken = os.Getenv(requiredTokenEnv)
	if cfg.APIToken == "" {
		return cfg, fmt.Errorf("missing %s", requiredTokenEnv)
	}

	cfg.ClusterName = os.Getenv(requiredClusterNameEnv)
	if cfg.ClusterName == "" {
		return cfg, fmt.Errorf("missing %s", requiredClusterNameEnv)
	}

	cfg.BaseURL = getenvOr(baseURLEnv, defaultBaseURL)
	cfg.RPS = getenvIntOr(rpsEnv, defaultRPS)
	cfg.LaunchMinInterval = getenvDurationSecondsOr(launchMinIntervalSecondsEnv, defaultLaunchMinInterval)
	cfg.InstanceTypeCacheTTL = getenvDurationOr(instanceTypeCacheTTLEnv, defaultInstanceTypeCacheTTL)

	return cfg, nil
}

func getenvOr(key, def string) string {
	val := os.Getenv(key)
	if val == "" {
		return def
	}
	return val
}

func getenvIntOr(key string, def int) int {
	val := os.Getenv(key)
	if val == "" {
		return def
	}
	parsed, err := strconv.Atoi(val)
	if err != nil {
		log.Log.Info("ignoring invalid env var, using default", "key", key, "value", val, "default", def)
		return def
	}
	if parsed <= 0 {
		log.Log.Info("ignoring non-positive env var, using default", "key", key, "value", val, "default", def)
		return def
	}
	return parsed
}

func getenvDurationSecondsOr(key string, def time.Duration) time.Duration {
	val := os.Getenv(key)
	if val == "" {
		return def
	}
	parsed, err := strconv.Atoi(val)
	if err != nil {
		log.Log.Info("ignoring invalid env var, using default", "key", key, "value", val, "default", def)
		return def
	}
	if parsed <= 0 {
		log.Log.Info("ignoring non-positive env var, using default", "key", key, "value", val, "default", def)
		return def
	}
	return time.Duration(parsed) * time.Second
}

func getenvDurationOr(key string, def time.Duration) time.Duration {
	val := os.Getenv(key)
	if val == "" {
		return def
	}
	parsed, err := time.ParseDuration(val)
	if err != nil {
		log.Log.Info("ignoring invalid env var, using default", "key", key, "value", val, "default", def)
		return def
	}
	if parsed <= 0 {
		log.Log.Info("ignoring non-positive env var, using default", "key", key, "value", val, "default", def)
		return def
	}
	return parsed
}
