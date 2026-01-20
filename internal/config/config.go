package config

import (
	"encoding/json"
)

type Config struct {
	ListenAddr string          `json:"listen_addr"`
	TLS        TLSConfig       `json:"tls"`
	Limits     LimitsConfig    `json:"limits"`
	Shutdown   ShutdownConfig  `json:"shutdown"`
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
	Overlay    bool        `json:"overlay"`
}

type Pool struct {
	Endpoints []string            `json:"endpoints"`
	Health    HealthConfig        `json:"health"`
	Breaker   BreakerConfig       `json:"breaker"`
	Outlier   OutlierConfig       `json:"outlier"`
	Transport PoolTransportConfig `json:"transport"`
	Overlay   bool                `json:"overlay"`
}

type RoutePolicy struct {
	RequestTimeoutMS                int                  `json:"request_timeout_ms"`
	UpstreamDialTimeoutMS           int                  `json:"upstream_dial_timeout_ms"`
	UpstreamResponseHeaderTimeoutMS int                  `json:"upstream_response_header_timeout_ms"`
	Retry                           RetryConfig          `json:"retry"`
	RetryBudget                     RetryBudgetConfig    `json:"retry_budget"`
	ClientRetryCap                  ClientRetryCapConfig `json:"client_retry_cap"`
	RequireMTLS                     bool                 `json:"require_mtls"`
	MTLSClientCA                    string               `json:"mtls_client_ca"`
	Cache                           CacheConfig          `json:"cache"`
	Traffic                         TrafficConfig        `json:"traffic"`
	Plugins                         PluginConfig         `json:"plugins"`
}

type TLSConfig struct {
	Enabled      bool      `json:"enabled"`
	Addr         string    `json:"addr"`
	Certs        []TLSCert `json:"certs"`
	ClientCAFile string    `json:"client_ca_file"`
	MinVersion   string    `json:"min_version"`
	CipherSuites []string  `json:"cipher_suites"`
}

type TLSCert struct {
	ServerName string `json:"server_name"`
	CertFile   string `json:"cert_file"`
	KeyFile    string `json:"key_file"`
}

type LimitsConfig struct {
	MaxHeaderBytes          int    `json:"max_header_bytes"`
	MaxHeaderCount          int    `json:"max_header_count"`
	MaxURLBytes             int    `json:"max_url_bytes"`
	MaxBodyBytes            *int64 `json:"max_body_bytes"`
	ReadHeaderTimeoutMS     int    `json:"read_header_timeout_ms"`
	ReadTimeoutMS           int    `json:"read_timeout_ms"`
	WriteTimeoutMS          int    `json:"write_timeout_ms"`
	IdleTimeoutMS           int    `json:"idle_timeout_ms"`
	ResponseStreamTimeoutMS int    `json:"response_stream_timeout_ms"`
}

type ShutdownConfig struct {
	DrainMS           int `json:"drain_ms"`
	GracefulTimeoutMS int `json:"graceful_timeout_ms"`
	ForceCloseMS      int `json:"force_close_ms"`
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

type CacheConfig struct {
	Enabled             bool     `json:"enabled"`
	Public              bool     `json:"public"`
	TTLMS               int      `json:"ttl_ms"`
	MaxObjectBytes      int      `json:"max_object_bytes"`
	VaryHeaders         []string `json:"vary_headers"`
	CoalesceEnabled     *bool    `json:"coalesce_enabled"`
	CoalesceTimeoutMS   int      `json:"coalesce_timeout_ms"`
	OnlyIfContentLength *bool    `json:"only_if_content_length"`
}

type TrafficConfig struct {
	Enabled      bool            `json:"enabled"`
	StablePool   string          `json:"stable_pool"`
	CanaryPool   string          `json:"canary_pool"`
	StableWeight int             `json:"stable_weight"`
	CanaryWeight int             `json:"canary_weight"`
	Cohort       CohortConfig    `json:"cohort"`
	Overload     OverloadConfig  `json:"overload"`
	AutoDrain    AutoDrainConfig `json:"autodrain"`
}

type PluginConfig struct {
	Enabled bool           `json:"enabled"`
	Filters []PluginFilter `json:"filters"`
}

type PluginFilter struct {
	Name              string              `json:"name"`
	Addr              string              `json:"addr"`
	RequestTimeoutMS  int                 `json:"request_timeout_ms"`
	ResponseTimeoutMS int                 `json:"response_timeout_ms"`
	FailureMode       string              `json:"failure_mode"`
	Breaker           PluginBreakerConfig `json:"breaker"`
}

type PluginBreakerConfig struct {
	Enabled             *bool `json:"enabled"`
	ConsecutiveFailures int   `json:"consecutive_failures"`
	OpenMS              int   `json:"open_ms"`
	HalfOpenProbes      int   `json:"half_open_probes"`
}

type CohortConfig struct {
	Enabled bool   `json:"enabled"`
	Key     string `json:"key"`
}

type OverloadConfig struct {
	Enabled        bool `json:"enabled"`
	MaxInflight    int  `json:"max_inflight"`
	MaxQueue       int  `json:"max_queue"`
	QueueTimeoutMS int  `json:"queue_timeout_ms"`
}

type AutoDrainConfig struct {
	Enabled             bool    `json:"enabled"`
	WindowMS            int     `json:"window_ms"`
	MinRequests         int     `json:"min_requests"`
	ErrorRateMultiplier float64 `json:"error_rate_multiplier"`
	CooloffMS           int     `json:"cooloff_ms"`
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

type PoolTransportConfig struct {
	MaxIdlePerHost    int `json:"max_idle_per_host"`
	MaxConnsPerHost   int `json:"max_conns_per_host"`
	IdleConnTimeoutMS int `json:"idle_conn_timeout_ms"`
}

type BreakerConfig struct {
	Enabled                     bool `json:"enabled"`
	FailureRateThresholdPercent int  `json:"failure_rate_threshold_percent"`
	MinimumRequests             int  `json:"minimum_requests"`
	EvaluationWindowMS          int  `json:"evaluation_window_ms"`
	OpenMS                      int  `json:"open_ms"`
	HalfOpenMaxProbes           int  `json:"half_open_max_probes"`
}

type OutlierConfig struct {
	Enabled                     bool `json:"enabled"`
	ConsecutiveFailures         int  `json:"consecutive_failures"`
	ErrorRateThreshold          int  `json:"error_rate_threshold_percent"`
	ErrorRateWindowMS           int  `json:"error_rate_window_ms"`
	MinRequests                 int  `json:"min_requests"`
	BaseEjectMS                 int  `json:"base_eject_ms"`
	MaxEjectMS                  int  `json:"max_eject_ms"`
	MaxEjectPercent             int  `json:"max_eject_percent"`
	LatencyEnabled              bool `json:"latency_enabled"`
	LatencyWindowSize           int  `json:"latency_window_size"`
	LatencyEvalIntervalMS       int  `json:"latency_eval_interval_ms"`
	LatencyMinSamples           int  `json:"latency_min_samples"`
	LatencyMultiplier           int  `json:"latency_multiplier"`
	LatencyConsecutiveIntervals int  `json:"latency_consecutive_intervals"`
}

func ParseJSON(data []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
