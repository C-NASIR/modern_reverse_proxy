package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const defaultMetricsTokenEnv = "METRICS_TOKEN"

func Validate(cfg *Config) ([]string, error) {
	if cfg == nil {
		return nil, errors.New("config is nil")
	}
	warnings := []string{}
	if err := validateLimits(cfg, &warnings); err != nil {
		return warnings, err
	}
	if err := validateMetrics(cfg); err != nil {
		return warnings, err
	}
	if err := validateRoutes(cfg, &warnings); err != nil {
		return warnings, err
	}
	if err := validatePools(cfg, &warnings); err != nil {
		return warnings, err
	}
	return warnings, nil
}

func ValidateMetricsToken(cfg *Config) error {
	return validateMetrics(cfg)
}

func validateLimits(cfg *Config, warnings *[]string) error {
	if cfg == nil {
		return errors.New("config is nil")
	}
	limitsConfigured := limitsConfigured(cfg.Limits)
	if cfg.Limits.MaxBodyBytes != nil {
		if *cfg.Limits.MaxBodyBytes <= 0 {
			return errors.New("limits.max_body_bytes must be > 0")
		}
	}
	if limitsConfigured && cfg.Limits.ReadHeaderTimeoutMS <= 0 {
		return errors.New("limits.read_header_timeout_ms must be > 0")
	}
	return nil
}

func validateMetrics(cfg *Config) error {
	if cfg == nil || cfg.Metrics == nil || !cfg.Metrics.RequireToken {
		return nil
	}
	env := strings.TrimSpace(cfg.Metrics.TokenEnv)
	if env == "" {
		env = defaultMetricsTokenEnv
	}
	if strings.TrimSpace(os.Getenv(env)) == "" {
		return fmt.Errorf("metrics token missing in %s", env)
	}
	return nil
}

func validateRoutes(cfg *Config, warnings *[]string) error {
	if cfg == nil {
		return errors.New("config is nil")
	}
	for _, route := range cfg.Routes {
		if route.Policy.Retry.Enabled && route.Policy.Retry.MaxAttempts <= 0 {
			return fmt.Errorf("route %q retry max_attempts must be > 0", route.ID)
		}
		if route.Policy.Traffic.Overload.Enabled && route.Policy.Traffic.Overload.MaxInflight <= 0 {
			return fmt.Errorf("route %q overload max_inflight must be > 0", route.ID)
		}
		if route.Policy.RequireMTLS {
			if !cfg.TLS.Enabled {
				return fmt.Errorf("route %q requires mtls but tls disabled", route.ID)
			}
			if strings.TrimSpace(cfg.TLS.ClientCAFile) == "" {
				return fmt.Errorf("route %q requires mtls but tls client_ca_file missing", route.ID)
			}
		}
		if route.Policy.Cache.Enabled {
			ttl := time.Duration(route.Policy.Cache.TTLMS) * time.Millisecond
			if ttl > time.Hour {
				*warnings = append(*warnings, fmt.Sprintf("route %q cache ttl_ms exceeds 1h", route.ID))
			}
		}
		if route.Policy.Plugins.Enabled && !route.Policy.RequireMTLS {
			for _, filter := range route.Policy.Plugins.Filters {
				if strings.EqualFold(strings.TrimSpace(filter.FailureMode), "fail_closed") {
					*warnings = append(*warnings, fmt.Sprintf("route %q uses fail_closed plugin %q on public route", route.ID, filter.Name))
					break
				}
			}
		}
	}
	return nil
}

func validatePools(cfg *Config, warnings *[]string) error {
	if cfg == nil {
		return errors.New("config is nil")
	}
	poolUsage := make(map[string]int)
	for _, route := range cfg.Routes {
		pools := referencedPools(route)
		for _, poolName := range pools {
			if poolName == "" {
				continue
			}
			poolUsage[poolName]++
		}
	}
	for name, poolCfg := range cfg.Pools {
		if poolCfg.Breaker.Enabled {
			continue
		}
		if poolUsage[name] > 1 {
			*warnings = append(*warnings, fmt.Sprintf("pool %q breaker disabled on multi-route pool", name))
		}
	}
	return nil
}

func referencedPools(route Route) []string {
	trafficCfg := route.Policy.Traffic
	if !trafficCfg.Enabled {
		return []string{route.Pool}
	}
	result := []string{}
	if strings.TrimSpace(trafficCfg.StablePool) != "" {
		result = append(result, trafficCfg.StablePool)
	}
	if strings.TrimSpace(trafficCfg.CanaryPool) != "" {
		result = append(result, trafficCfg.CanaryPool)
	}
	return result
}

func limitsConfigured(cfg LimitsConfig) bool {
	if cfg.MaxHeaderBytes != 0 || cfg.MaxHeaderCount != 0 || cfg.MaxURLBytes != 0 {
		return true
	}
	if cfg.MaxBodyBytes != nil {
		return true
	}
	if cfg.ReadHeaderTimeoutMS != 0 || cfg.ReadTimeoutMS != 0 || cfg.WriteTimeoutMS != 0 {
		return true
	}
	if cfg.IdleTimeoutMS != 0 || cfg.ResponseStreamTimeoutMS != 0 {
		return true
	}
	return false
}
