package config

import (
	"encoding/json"
)

type Config struct {
	ListenAddr string          `json:"listen_addr"`
	Routes     []Route         `json:"routes"`
	Pools      map[string]Pool `json:"pools"`
}

type Route struct {
	ID         string      `json:"id"`
	Host       string      `json:"host"`
	PathPrefix string      `json:"path_prefix"`
	Methods    []string    `json:"methods"`
	Pool       string      `json:"pool"`
	Policy     RoutePolicy `json:"policy"`
}

type Pool struct {
	Endpoints []string     `json:"endpoints"`
	Health    HealthConfig `json:"health"`
}

type RoutePolicy struct {
	RequestTimeoutMS                int                  `json:"request_timeout_ms"`
	UpstreamDialTimeoutMS           int                  `json:"upstream_dial_timeout_ms"`
	UpstreamResponseHeaderTimeoutMS int                  `json:"upstream_response_header_timeout_ms"`
	Retry                           RetryConfig          `json:"retry"`
	RetryBudget                     RetryBudgetConfig    `json:"retry_budget"`
	ClientRetryCap                  ClientRetryCapConfig `json:"client_retry_cap"`
}

type RetryConfig struct {
	Enabled            bool     `json:"enabled"`
	MaxAttempts        int      `json:"max_attempts"`
	PerTryTimeoutMS    int      `json:"per_try_timeout_ms"`
	TotalRetryBudgetMS int      `json:"total_retry_budget_ms"`
	RetryOnStatus      []int    `json:"retry_on_status"`
	RetryOnErrors      []string `json:"retry_on_errors"`
	BackoffMS          int      `json:"backoff_ms"`
	BackoffJitterMS    int      `json:"backoff_jitter_ms"`
}

type RetryBudgetConfig struct {
	Enabled            bool `json:"enabled"`
	PercentOfSuccesses int  `json:"percent_of_successes"`
	Burst              int  `json:"burst"`
}

type ClientRetryCapConfig struct {
	Enabled            bool   `json:"enabled"`
	Key                string `json:"key"`
	PercentOfSuccesses int    `json:"percent_of_successes"`
	Burst              int    `json:"burst"`
	LRUSize            int    `json:"lru_size"`
}

type HealthConfig struct {
	Path                   string `json:"path"`
	IntervalMS             int    `json:"interval_ms"`
	TimeoutMS              int    `json:"timeout_ms"`
	UnhealthyAfterFailures int    `json:"unhealthy_after_failures"`
	HealthyAfterSuccesses  int    `json:"healthy_after_successes"`
	BaseEjectMS            int    `json:"base_eject_ms"`
	MaxEjectMS             int    `json:"max_eject_ms"`
}

func ParseJSON(data []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
