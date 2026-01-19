package runtime

import (
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"modern_reverse_proxy/internal/breaker"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/health"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/outlier"
	"modern_reverse_proxy/internal/policy"
	"modern_reverse_proxy/internal/pool"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/router"
	"modern_reverse_proxy/internal/tlsstore"
)

type Snapshot struct {
	Router      *router.Router
	Pools       map[string]pool.PoolKey
	PoolConfigs map[string]PoolConfig
	TLSEnabled  bool
	TLSStore    *tlsstore.Store
	TLSConfig   *tls.Config
	TLSAddr     string
	Version     string
	CreatedAt   time.Time
	Source      string
}

type PoolConfig struct {
	Breaker breaker.Config
	Outlier outlier.Config
}

type Store struct {
	v atomic.Value
}

func NewStore(initial *Snapshot) *Store {
	store := &Store{}
	store.v.Store(initial)
	return store
}

func (s *Store) Get() *Snapshot {
	if s == nil {
		return nil
	}
	value := s.v.Load()
	if value == nil {
		return nil
	}
	return value.(*Snapshot)
}

func (s *Store) Swap(next *Snapshot) {
	if s == nil {
		return
	}
	s.v.Store(next)
}

const (
	defaultRequestTimeout                = 30 * time.Second
	defaultUpstreamDialTimeout           = time.Second
	defaultUpstreamResponseHeaderTimeout = 5 * time.Second
	defaultHealthPath                    = "/healthz"
	defaultHealthInterval                = 5 * time.Second
	defaultHealthTimeout                 = time.Second
	defaultUnhealthyAfterFailures        = 3
	defaultHealthyAfterSuccesses         = 2
	defaultBaseEject                     = 10 * time.Second
	defaultMaxEject                      = 5 * time.Minute
	defaultRetryMaxAttempts              = 1
	defaultRetryClientLRUSize            = 10000
	defaultBreakerFailureRateThreshold   = 50
	defaultBreakerMinRequests            = 20
	defaultBreakerEvalWindow             = 10 * time.Second
	defaultBreakerOpenDuration           = 2 * time.Second
	defaultBreakerHalfOpenMaxProbes      = 5
	defaultOutlierConsecutiveFailures    = 5
	defaultOutlierErrorRateThreshold     = 50
	defaultOutlierErrorRateWindow        = 30 * time.Second
	defaultOutlierMinRequests            = 20
	defaultOutlierBaseEject              = 30 * time.Second
	defaultOutlierMaxEject               = 10 * time.Minute
	defaultOutlierMaxEjectPercent        = 50
	defaultOutlierLatencyWindowSize      = 128
	defaultOutlierLatencyEvalInterval    = 10 * time.Second
	defaultOutlierLatencyMinSamples      = 50
	defaultOutlierLatencyMultiplier      = 3
	defaultOutlierLatencyConsecutive     = 3
	defaultTLSAddr                       = "127.0.0.1:8443"
)

var (
	defaultRetryStatuses = []int{502, 503, 504}
	defaultRetryErrors   = []string{"dial", "timeout"}
)

func BuildSnapshot(cfg *config.Config, reg *registry.Registry, breakerReg *breaker.Registry, outlierReg *outlier.Registry) (*Snapshot, error) {
	_ = breakerReg
	success := false
	defer func() {
		metrics := obs.DefaultMetrics()
		if metrics == nil {
			return
		}
		if success {
			metrics.RecordConfigApply("success")
			return
		}
		metrics.RecordConfigApply("rejected")
	}()

	if cfg == nil {
		return nil, errors.New("config is nil")
	}
	if reg == nil {
		return nil, errors.New("registry is nil")
	}

	pools := make(map[string]pool.PoolKey, len(cfg.Pools))
	poolConfigs := make(map[string]PoolConfig, len(cfg.Pools))
	for name, poolCfg := range cfg.Pools {
		if len(poolCfg.Endpoints) == 0 {
			return nil, fmt.Errorf("pool %q has no endpoints", name)
		}
		poolKey := pool.PoolKey(name)
		pools[name] = poolKey

		healthCfg := health.Config{
			Path:                   stringOrDefault(poolCfg.Health.Path, defaultHealthPath),
			Interval:               durationOrDefault(poolCfg.Health.IntervalMS, defaultHealthInterval),
			Timeout:                durationOrDefault(poolCfg.Health.TimeoutMS, defaultHealthTimeout),
			UnhealthyAfterFailures: intOrDefault(poolCfg.Health.UnhealthyAfterFailures, defaultUnhealthyAfterFailures),
			HealthyAfterSuccesses:  intOrDefault(poolCfg.Health.HealthyAfterSuccesses, defaultHealthyAfterSuccesses),
			BaseEjectDuration:      durationOrDefault(poolCfg.Health.BaseEjectMS, defaultBaseEject),
			MaxEjectDuration:       durationOrDefault(poolCfg.Health.MaxEjectMS, defaultMaxEject),
		}

		reg.Reconcile(poolKey, poolCfg.Endpoints, healthCfg)

		poolConfigs[name] = PoolConfig{
			Breaker: breaker.Config{
				Enabled:                     poolCfg.Breaker.Enabled,
				FailureRateThresholdPercent: intOrDefault(poolCfg.Breaker.FailureRateThresholdPercent, defaultBreakerFailureRateThreshold),
				MinimumRequests:             intOrDefault(poolCfg.Breaker.MinimumRequests, defaultBreakerMinRequests),
				EvaluationWindow:            durationOrDefault(poolCfg.Breaker.EvaluationWindowMS, defaultBreakerEvalWindow),
				OpenDuration:                durationOrDefault(poolCfg.Breaker.OpenMS, defaultBreakerOpenDuration),
				HalfOpenMaxProbes:           intOrDefault(poolCfg.Breaker.HalfOpenMaxProbes, defaultBreakerHalfOpenMaxProbes),
			},
			Outlier: outlier.Config{
				Enabled:                     poolCfg.Outlier.Enabled,
				ConsecutiveFailures:         intOrDefault(poolCfg.Outlier.ConsecutiveFailures, defaultOutlierConsecutiveFailures),
				ErrorRateThresholdPercent:   intOrDefault(poolCfg.Outlier.ErrorRateThreshold, defaultOutlierErrorRateThreshold),
				ErrorRateWindow:             durationOrDefault(poolCfg.Outlier.ErrorRateWindowMS, defaultOutlierErrorRateWindow),
				MinRequests:                 intOrDefault(poolCfg.Outlier.MinRequests, defaultOutlierMinRequests),
				BaseEjectDuration:           durationOrDefault(poolCfg.Outlier.BaseEjectMS, defaultOutlierBaseEject),
				MaxEjectDuration:            durationOrDefault(poolCfg.Outlier.MaxEjectMS, defaultOutlierMaxEject),
				MaxEjectPercent:             intOrDefault(poolCfg.Outlier.MaxEjectPercent, defaultOutlierMaxEjectPercent),
				LatencyEnabled:              poolCfg.Outlier.LatencyEnabled,
				LatencyWindowSize:           intOrDefault(poolCfg.Outlier.LatencyWindowSize, defaultOutlierLatencyWindowSize),
				LatencyEvalInterval:         durationOrDefault(poolCfg.Outlier.LatencyEvalIntervalMS, defaultOutlierLatencyEvalInterval),
				LatencyMinSamples:           intOrDefault(poolCfg.Outlier.LatencyMinSamples, defaultOutlierLatencyMinSamples),
				LatencyMultiplier:           intOrDefault(poolCfg.Outlier.LatencyMultiplier, defaultOutlierLatencyMultiplier),
				LatencyConsecutiveIntervals: intOrDefault(poolCfg.Outlier.LatencyConsecutiveIntervals, defaultOutlierLatencyConsecutive),
			},
		}
	}

	seenIDs := make(map[string]struct{}, len(cfg.Routes))
	routes := make([]policy.Route, 0, len(cfg.Routes))
	requiresMTLS := false
	for _, route := range cfg.Routes {
		if route.Host == "" {
			return nil, fmt.Errorf("route %q host is empty", route.ID)
		}
		if !strings.HasPrefix(route.PathPrefix, "/") {
			return nil, fmt.Errorf("route %q path prefix must start with /", route.ID)
		}
		if _, ok := seenIDs[route.ID]; ok {
			return nil, fmt.Errorf("route id %q is not unique", route.ID)
		}
		seenIDs[route.ID] = struct{}{}

		if _, ok := pools[route.Pool]; !ok {
			return nil, fmt.Errorf("route %q references missing pool %q", route.ID, route.Pool)
		}

		methods := make(map[string]bool)
		for _, method := range route.Methods {
			if method == "" {
				continue
			}
			methods[strings.ToUpper(method)] = true
		}
		if len(methods) == 0 {
			methods = nil
		}

		if route.Policy.RequireMTLS && route.Policy.MTLSClientCA != "" && route.Policy.MTLSClientCA != "default" {
			return nil, fmt.Errorf("route %q mtls_client_ca must be default", route.ID)
		}
		if route.Policy.RequireMTLS {
			requiresMTLS = true
		}

		policyRuntime := policy.Policy{
			RequestTimeout:                durationOrDefault(route.Policy.RequestTimeoutMS, defaultRequestTimeout),
			UpstreamDialTimeout:           durationOrDefault(route.Policy.UpstreamDialTimeoutMS, defaultUpstreamDialTimeout),
			UpstreamResponseHeaderTimeout: durationOrDefault(route.Policy.UpstreamResponseHeaderTimeoutMS, defaultUpstreamResponseHeaderTimeout),
			Retry: policy.RetryPolicy{
				Enabled:          route.Policy.Retry.Enabled,
				MaxAttempts:      intOrDefault(route.Policy.Retry.MaxAttempts, defaultRetryMaxAttempts),
				PerTryTimeout:    durationOrZero(route.Policy.Retry.PerTryTimeoutMS),
				TotalRetryBudget: durationOrZero(route.Policy.Retry.TotalRetryBudgetMS),
				RetryOnStatus:    retryStatusMap(route.Policy.Retry.RetryOnStatus),
				RetryOnErrors:    retryErrorMap(route.Policy.Retry.RetryOnErrors),
				Backoff:          durationOrZero(route.Policy.Retry.BackoffMS),
				BackoffJitter:    durationOrZero(route.Policy.Retry.BackoffJitterMS),
			},
			RetryBudget: policy.RetryBudgetPolicy{
				Enabled:            route.Policy.RetryBudget.Enabled,
				PercentOfSuccesses: nonNegative(route.Policy.RetryBudget.PercentOfSuccesses),
				Burst:              nonNegative(route.Policy.RetryBudget.Burst),
			},
			ClientRetryCap: policy.ClientRetryCapPolicy{
				Enabled:            route.Policy.ClientRetryCap.Enabled,
				Key:                route.Policy.ClientRetryCap.Key,
				PercentOfSuccesses: nonNegative(route.Policy.ClientRetryCap.PercentOfSuccesses),
				Burst:              nonNegative(route.Policy.ClientRetryCap.Burst),
				LRUSize:            intOrDefault(route.Policy.ClientRetryCap.LRUSize, defaultRetryClientLRUSize),
			},
			RequireMTLS:  route.Policy.RequireMTLS,
			MTLSClientCA: route.Policy.MTLSClientCA,
		}

		stablePoolKey := fmt.Sprintf("%s::%s", route.ID, route.Pool)
		if outlierReg != nil {
			poolCfg := cfg.Pools[route.Pool]
			outlierCfg := poolConfigs[route.Pool].Outlier
			outlierReg.Reconcile(stablePoolKey, poolCfg.Endpoints, outlierCfg)
		}

		routes = append(routes, policy.Route{
			ID:            route.ID,
			Host:          route.Host,
			PathPrefix:    route.PathPrefix,
			Methods:       methods,
			PoolName:      route.Pool,
			StablePoolKey: stablePoolKey,
			Policy:        policyRuntime,
		})
	}

	compiled := router.NewRouter(routes)
	if compiled == nil {
		return nil, errors.New("router build failed")
	}

	var tlsStore *tlsstore.Store
	var tlsConfig *tls.Config
	tlsAddr := ""
	if cfg.TLS.Enabled {
		if len(cfg.TLS.Certs) == 0 {
			return nil, errors.New("tls enabled but no certs configured")
		}
		if requiresMTLS && cfg.TLS.ClientCAFile == "" {
			return nil, errors.New("mtls required but client_ca_file missing")
		}
		certSpecs := make([]tlsstore.CertSpec, 0, len(cfg.TLS.Certs))
		for _, cert := range cfg.TLS.Certs {
			certSpecs = append(certSpecs, tlsstore.CertSpec{
				ServerName: cert.ServerName,
				CertFile:   cert.CertFile,
				KeyFile:    cert.KeyFile,
			})
		}
		var err error
		tlsStore, err = tlsstore.LoadStore(certSpecs, cfg.TLS.ClientCAFile)
		if err != nil {
			return nil, err
		}
		minVersion, err := parseTLSMinVersion(cfg.TLS.MinVersion)
		if err != nil {
			return nil, err
		}
		cipherSuites, err := parseCipherSuites(cfg.TLS.CipherSuites)
		if err != nil {
			return nil, err
		}
		tlsConfig = &tls.Config{
			GetCertificate: tlsStore.GetCertificate,
			MinVersion:     minVersion,
			ClientAuth:     tls.RequestClientCert,
			NextProtos:     []string{"h2", "http/1.1"},
		}
		if len(cipherSuites) > 0 {
			tlsConfig.CipherSuites = cipherSuites
		}
		tlsAddr = cfg.TLS.Addr
		if tlsAddr == "" {
			tlsAddr = defaultTLSAddr
		}
	}
	if requiresMTLS && !cfg.TLS.Enabled {
		return nil, errors.New("mtls required but tls disabled")
	}

	snapshot := &Snapshot{
		Router:      compiled,
		Pools:       pools,
		PoolConfigs: poolConfigs,
		TLSEnabled:  cfg.TLS.Enabled,
		TLSStore:    tlsStore,
		TLSConfig:   tlsConfig,
		TLSAddr:     tlsAddr,
		Version:     fmt.Sprintf("v-%d", time.Now().UnixNano()),
		CreatedAt:   time.Now().UTC(),
		Source:      "file",
	}
	success = true
	return snapshot, nil
}

func durationOrDefault(ms int, fallback time.Duration) time.Duration {
	if ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func durationOrZero(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func intOrDefault(value int, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func stringOrDefault(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func retryStatusMap(values []int) map[int]bool {
	if len(values) == 0 {
		values = defaultRetryStatuses
	}
	result := make(map[int]bool, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		result[value] = true
	}
	return result
}

func retryErrorMap(values []string) map[string]bool {
	if len(values) == 0 {
		values = defaultRetryErrors
	}
	result := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		result[value] = true
	}
	return result
}

func parseTLSMinVersion(value string) (uint16, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "1.2" {
		return tls.VersionTLS12, nil
	}
	if trimmed == "1.3" {
		return tls.VersionTLS13, nil
	}
	return 0, fmt.Errorf("unsupported tls min_version %q", value)
}

func parseCipherSuites(values []string) ([]uint16, error) {
	if len(values) == 0 {
		return nil, nil
	}
	suiteMap := make(map[string]uint16)
	for _, suite := range tls.CipherSuites() {
		suiteMap[suite.Name] = suite.ID
	}
	for _, suite := range tls.InsecureCipherSuites() {
		suiteMap[suite.Name] = suite.ID
	}

	result := make([]uint16, 0, len(values))
	for _, name := range values {
		if name == "" {
			continue
		}
		id, ok := suiteMap[name]
		if !ok {
			return nil, fmt.Errorf("unknown tls cipher suite %q", name)
		}
		result = append(result, id)
	}
	return result, nil
}
