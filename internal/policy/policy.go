package policy

import (
	"time"

	"modern_reverse_proxy/internal/plugin"
	"modern_reverse_proxy/internal/traffic"
)

type Policy struct {
	RequestTimeout                time.Duration
	UpstreamDialTimeout           time.Duration
	UpstreamResponseHeaderTimeout time.Duration
	Retry                         RetryPolicy
	RetryBudget                   RetryBudgetPolicy
	ClientRetryCap                ClientRetryCapPolicy
	RequireMTLS                   bool
	MTLSClientCA                  string
	Cache                         CachePolicy
	Plugins                       plugin.Policy
}

type RetryPolicy struct {
	Enabled          bool
	MaxAttempts      int
	PerTryTimeout    time.Duration
	TotalRetryBudget time.Duration
	RetryOnStatus    map[int]bool
	RetryOnErrors    map[string]bool
	Backoff          time.Duration
	BackoffJitter    time.Duration
}

type RetryBudgetPolicy struct {
	Enabled            bool
	PercentOfSuccesses int
	Burst              int
}

type ClientRetryCapPolicy struct {
	Enabled            bool
	Key                string
	PercentOfSuccesses int
	Burst              int
	LRUSize            int
}

type CachePolicy struct {
	Enabled             bool
	Public              bool
	TTL                 time.Duration
	MaxObjectBytes      int64
	VaryHeaders         []string
	CoalesceEnabled     bool
	CoalesceTimeout     time.Duration
	OnlyIfContentLength bool
}

type Route struct {
	ID             string
	Host           string
	PathPrefix     string
	Methods        map[string]bool
	PoolName       string
	CanaryPoolName string
	StablePoolKey  string
	CanaryPoolKey  string
	TrafficPlan    *traffic.Plan
	Policy         Policy
}
