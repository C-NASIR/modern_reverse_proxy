package policy

import "time"

type Policy struct {
	RequestTimeout                time.Duration
	UpstreamDialTimeout           time.Duration
	UpstreamResponseHeaderTimeout time.Duration
	Retry                         RetryPolicy
	RetryBudget                   RetryBudgetPolicy
	ClientRetryCap                ClientRetryCapPolicy
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

type Route struct {
	ID         string
	Host       string
	PathPrefix string
	Methods    map[string]bool
	PoolName   string
	Policy     Policy
}
