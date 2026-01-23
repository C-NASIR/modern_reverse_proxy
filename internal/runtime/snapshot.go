package runtime

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"modern_reverse_proxy/internal/breaker"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/health"
	"modern_reverse_proxy/internal/limits"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/outlier"
	"modern_reverse_proxy/internal/plugin"
	"modern_reverse_proxy/internal/policy"
	"modern_reverse_proxy/internal/pool"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/router"
	"modern_reverse_proxy/internal/tlsstore"
	"modern_reverse_proxy/internal/traffic"
	"modern_reverse_proxy/internal/transport"
)

type Snapshot struct {
	ID          uint64
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
	RouteCount  int
	Limits      limits.Limits
	Logging     config.LoggingConfig
	refCount    atomic.Int64
	retiredAt   atomic.Int64
}

type PoolConfig struct {
	Breaker breaker.Config
	Outlier outlier.Config
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
	defaultCacheMaxObjectBytes           = int64(1024 * 1024)
	defaultCacheMinObjectBytes           = int64(1024)
	defaultCacheMaxObjectBytesLimit      = int64(50 * 1024 * 1024)
	defaultCacheVaryHeaderMaxLen         = 128
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
	defaultCacheCoalesceTimeout          = 5 * time.Second
	defaultTLSAddr                       = "127.0.0.1:8443"
	defaultTrafficStableWeight           = 100
	defaultPluginRequestTimeout          = 50 * time.Millisecond
	defaultPluginResponseTimeout         = 50 * time.Millisecond
	defaultPluginBreakerOpenDuration     = 2 * time.Second
	maxPluginFilters                     = 200
	defaultPluginBreakerConsecutiveFails = 5
	defaultPluginBreakerHalfOpenProbes   = 3
	defaultPoolMaxIdlePerHost            = 256
	defaultPoolIdleConnTimeout           = 90 * time.Second
)

var (
	defaultRetryStatuses = []int{502, 503, 504}
	defaultRetryErrors   = []string{"dial", "timeout"}
	snapshotIDCounter    atomic.Uint64
)

func BuildSnapshot(cfg *config.Config, reg *registry.Registry, breakerReg *breaker.Registry, outlierReg *outlier.Registry, trafficReg *traffic.Registry) (*Snapshot, error) {
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

	limitConfig, err := limits.FromConfig(cfg.Limits)
	if err != nil {
		return nil, err
	}
	if trafficReg == nil {
		trafficReg = traffic.NewRegistry(0, 0)
	}

	pools := make(map[string]pool.PoolKey, len(cfg.Pools))
	poolConfigs := make(map[string]PoolConfig, len(cfg.Pools))
	desiredPools := make(map[pool.PoolKey]struct{}, len(cfg.Pools))
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

		transportOpts := transport.Options{
			MaxIdleConnsPerHost: intOrDefault(poolCfg.Transport.MaxIdlePerHost, defaultPoolMaxIdlePerHost),
			MaxConnsPerHost:     nonNegative(poolCfg.Transport.MaxConnsPerHost),
			IdleConnTimeout:     durationOrDefault(poolCfg.Transport.IdleConnTimeoutMS, defaultPoolIdleConnTimeout),
		}
		reg.Reconcile(poolKey, poolCfg.Endpoints, healthCfg, transportOpts)
		desiredPools[poolKey] = struct{}{}

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
	reg.PrunePools(desiredPools)

	seenIDs := make(map[string]struct{}, len(cfg.Routes))
	filterNames := make(map[string]struct{})
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

		pluginPolicy, err := pluginPolicyFromConfig(route.ID, route.Policy.Plugins, filterNames)
		if err != nil {
			return nil, err
		}
		policyRuntime.Plugins = pluginPolicy

		cachePolicy, err := cachePolicyFromConfig(route.ID, route.Policy.Cache)
		if err != nil {
			return nil, err
		}
		policyRuntime.Cache = cachePolicy

		trafficCfg, stablePoolName, canaryPoolName, err := trafficConfigFromRoute(route.ID, route.Policy.Traffic)
		if err != nil {
			return nil, err
		}

		stablePoolKey := ""
		canaryPoolKey := ""
		trafficPlan := (*traffic.Plan)(nil)
		if trafficCfg.Enabled {
			if _, ok := pools[stablePoolName]; !ok {
				return nil, fmt.Errorf("route %q traffic stable_pool missing pool %q", route.ID, stablePoolName)
			}
			if canaryPoolName != "" {
				if _, ok := pools[canaryPoolName]; !ok {
					return nil, fmt.Errorf("route %q traffic canary_pool missing pool %q", route.ID, canaryPoolName)
				}
			}
			stablePoolKey = fmt.Sprintf("%s::%s", route.ID, stablePoolName)
			if canaryPoolName != "" {
				canaryPoolKey = fmt.Sprintf("%s::%s", route.ID, canaryPoolName)
			}
			trafficPlan, err = trafficReg.Plan(route.ID, trafficCfg)
			if err != nil {
				return nil, err
			}
			if outlierReg != nil {
				poolCfg := cfg.Pools[stablePoolName]
				outlierCfg := poolConfigs[stablePoolName].Outlier
				outlierReg.Reconcile(stablePoolKey, poolCfg.Endpoints, outlierCfg)
				if canaryPoolName != "" {
					poolCfg = cfg.Pools[canaryPoolName]
					outlierCfg = poolConfigs[canaryPoolName].Outlier
					outlierReg.Reconcile(canaryPoolKey, poolCfg.Endpoints, outlierCfg)
				}
			}
		} else {
			stablePoolName = route.Pool
			stablePoolKey = fmt.Sprintf("%s::%s", route.ID, route.Pool)
			if outlierReg != nil {
				poolCfg := cfg.Pools[route.Pool]
				outlierCfg := poolConfigs[route.Pool].Outlier
				outlierReg.Reconcile(stablePoolKey, poolCfg.Endpoints, outlierCfg)
			}
		}

		routes = append(routes, policy.Route{
			ID:             route.ID,
			Host:           route.Host,
			PathPrefix:     route.PathPrefix,
			Methods:        methods,
			PoolName:       stablePoolName,
			CanaryPoolName: canaryPoolName,
			StablePoolKey:  stablePoolKey,
			CanaryPoolKey:  canaryPoolKey,
			TrafficPlan:    trafficPlan,
			Policy:         policyRuntime,
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
		ID:          nextSnapshotID(),
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
		RouteCount:  len(routes),
		Limits:      limitConfig,
		Logging:     cfg.Logging,
	}
	success = true
	return snapshot, nil
}

func nextSnapshotID() uint64 {
	return snapshotIDCounter.Add(1)
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

func cachePolicyFromConfig(routeID string, cacheCfg config.CacheConfig) (policy.CachePolicy, error) {
	maxObjectBytes := int64(cacheCfg.MaxObjectBytes)
	if maxObjectBytes <= 0 {
		maxObjectBytes = defaultCacheMaxObjectBytes
	}
	if maxObjectBytes < defaultCacheMinObjectBytes || maxObjectBytes > defaultCacheMaxObjectBytesLimit {
		return policy.CachePolicy{}, fmt.Errorf("route %q cache max_object_bytes out of range", routeID)
	}

	varyHeaders, err := sanitizeVaryHeaders(routeID, cacheCfg.VaryHeaders)
	if err != nil {
		return policy.CachePolicy{}, err
	}

	coalesceEnabled := boolOrDefault(cacheCfg.CoalesceEnabled, true)
	onlyIfContentLength := boolOrDefault(cacheCfg.OnlyIfContentLength, true)
	coalesceTimeout := durationOrDefault(cacheCfg.CoalesceTimeoutMS, defaultCacheCoalesceTimeout)
	ttl := durationOrZero(cacheCfg.TTLMS)
	if cacheCfg.Enabled && ttl <= 0 {
		return policy.CachePolicy{}, fmt.Errorf("route %q cache ttl_ms must be > 0", routeID)
	}

	return policy.CachePolicy{
		Enabled:             cacheCfg.Enabled,
		Public:              cacheCfg.Public,
		TTL:                 ttl,
		MaxObjectBytes:      maxObjectBytes,
		VaryHeaders:         varyHeaders,
		CoalesceEnabled:     coalesceEnabled,
		CoalesceTimeout:     coalesceTimeout,
		OnlyIfContentLength: onlyIfContentLength,
	}, nil
}

func pluginPolicyFromConfig(routeID string, pluginCfg config.PluginConfig, filterNames map[string]struct{}) (plugin.Policy, error) {
	filters := make([]plugin.Filter, 0, len(pluginCfg.Filters))
	if pluginCfg.Enabled && len(pluginCfg.Filters) == 0 {
		return plugin.Policy{}, fmt.Errorf("route %q plugins enabled but no filters", routeID)
	}

	for _, filter := range pluginCfg.Filters {
		name := strings.TrimSpace(filter.Name)
		if name == "" {
			return plugin.Policy{}, fmt.Errorf("route %q plugin filter name required", routeID)
		}
		addr := strings.TrimSpace(filter.Addr)
		if addr == "" {
			return plugin.Policy{}, fmt.Errorf("route %q plugin filter %q addr required", routeID, name)
		}
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return plugin.Policy{}, fmt.Errorf("route %q plugin filter %q addr must be host:port", routeID, name)
		}
		if filterNames != nil {
			filterNames[name] = struct{}{}
			if len(filterNames) > maxPluginFilters {
				return plugin.Policy{}, fmt.Errorf("plugin filter count exceeds %d", maxPluginFilters)
			}
		}

		requestTimeout := durationOrDefault(filter.RequestTimeoutMS, defaultPluginRequestTimeout)
		if requestTimeout <= 0 {
			return plugin.Policy{}, fmt.Errorf("route %q plugin filter %q request_timeout_ms must be > 0", routeID, name)
		}
		responseTimeout := durationOrDefault(filter.ResponseTimeoutMS, defaultPluginResponseTimeout)
		if responseTimeout <= 0 {
			return plugin.Policy{}, fmt.Errorf("route %q plugin filter %q response_timeout_ms must be > 0", routeID, name)
		}

		failureMode, err := parseFailureMode(filter.FailureMode)
		if err != nil {
			return plugin.Policy{}, fmt.Errorf("route %q plugin filter %q %v", routeID, name, err)
		}

		breakerEnabled := boolOrDefault(filter.Breaker.Enabled, true)
		breaker := plugin.BreakerConfig{
			Enabled:             breakerEnabled,
			ConsecutiveFailures: intOrDefault(filter.Breaker.ConsecutiveFailures, defaultPluginBreakerConsecutiveFails),
			OpenDuration:        durationOrDefault(filter.Breaker.OpenMS, defaultPluginBreakerOpenDuration),
			HalfOpenProbes:      intOrDefault(filter.Breaker.HalfOpenProbes, defaultPluginBreakerHalfOpenProbes),
		}

		filters = append(filters, plugin.Filter{
			Name:            name,
			Addr:            addr,
			RequestTimeout:  requestTimeout,
			ResponseTimeout: responseTimeout,
			FailureMode:     failureMode,
			Breaker:         breaker,
		})
	}

	return plugin.Policy{
		Enabled: pluginCfg.Enabled,
		Filters: filters,
	}, nil
}

func parseFailureMode(mode string) (plugin.FailureMode, error) {
	trimmed := strings.TrimSpace(strings.ToLower(mode))
	if trimmed == "" {
		return plugin.FailureModeFailOpen, nil
	}
	switch trimmed {
	case string(plugin.FailureModeFailOpen):
		return plugin.FailureModeFailOpen, nil
	case string(plugin.FailureModeFailClose):
		return plugin.FailureModeFailClose, nil
	default:
		return "", fmt.Errorf("failure_mode must be fail_open or fail_closed")
	}
}

func trafficConfigFromRoute(routeID string, cfg config.TrafficConfig) (traffic.Config, string, string, error) {
	if !cfg.Enabled {
		return traffic.Config{}, "", "", nil
	}
	stablePool := strings.TrimSpace(cfg.StablePool)
	if stablePool == "" {
		return traffic.Config{}, "", "", fmt.Errorf("route %q traffic stable_pool missing", routeID)
	}
	canaryPool := strings.TrimSpace(cfg.CanaryPool)
	stableWeight := cfg.StableWeight
	canaryWeight := cfg.CanaryWeight
	if stableWeight < 0 || canaryWeight < 0 {
		return traffic.Config{}, "", "", fmt.Errorf("route %q traffic weights must be non-negative", routeID)
	}
	if canaryPool == "" && stableWeight == 0 && canaryWeight == 0 {
		stableWeight = defaultTrafficStableWeight
	}
	if canaryPool == "" && canaryWeight > 0 {
		return traffic.Config{}, "", "", fmt.Errorf("route %q traffic canary_pool required when canary_weight > 0", routeID)
	}
	if stableWeight == 0 && canaryWeight == 0 {
		return traffic.Config{}, "", "", fmt.Errorf("route %q traffic weights cannot both be zero", routeID)
	}
	if cfg.Cohort.Enabled && strings.TrimSpace(cfg.Cohort.Key) == "" {
		return traffic.Config{}, "", "", fmt.Errorf("route %q traffic cohort key missing", routeID)
	}
	if cfg.Overload.Enabled {
		if cfg.Overload.MaxInflight <= 0 {
			return traffic.Config{}, "", "", fmt.Errorf("route %q traffic overload max_inflight must be > 0", routeID)
		}
		if cfg.Overload.MaxQueue < 0 {
			return traffic.Config{}, "", "", fmt.Errorf("route %q traffic overload max_queue must be >= 0", routeID)
		}
		if cfg.Overload.MaxQueue > 0 && cfg.Overload.QueueTimeoutMS <= 0 {
			return traffic.Config{}, "", "", fmt.Errorf("route %q traffic overload queue_timeout_ms must be > 0", routeID)
		}
	}
	if cfg.AutoDrain.Enabled {
		if cfg.AutoDrain.WindowMS <= 0 {
			return traffic.Config{}, "", "", fmt.Errorf("route %q traffic autodrain window_ms must be > 0", routeID)
		}
		if cfg.AutoDrain.MinRequests <= 0 {
			return traffic.Config{}, "", "", fmt.Errorf("route %q traffic autodrain min_requests must be > 0", routeID)
		}
		if cfg.AutoDrain.ErrorRateMultiplier <= 0 {
			return traffic.Config{}, "", "", fmt.Errorf("route %q traffic autodrain error_rate_multiplier must be > 0", routeID)
		}
		if cfg.AutoDrain.CooloffMS <= 0 {
			return traffic.Config{}, "", "", fmt.Errorf("route %q traffic autodrain cooloff_ms must be > 0", routeID)
		}
	}

	return traffic.Config{
		Enabled:      cfg.Enabled,
		StableWeight: stableWeight,
		CanaryWeight: canaryWeight,
		Cohort: traffic.CohortConfig{
			Enabled: cfg.Cohort.Enabled,
			Key:     strings.TrimSpace(cfg.Cohort.Key),
		},
		Overload: traffic.OverloadConfig{
			Enabled:      cfg.Overload.Enabled,
			MaxInflight:  cfg.Overload.MaxInflight,
			MaxQueue:     cfg.Overload.MaxQueue,
			QueueTimeout: durationOrZero(cfg.Overload.QueueTimeoutMS),
		},
		AutoDrain: traffic.AutoDrainConfig{
			Enabled:             cfg.AutoDrain.Enabled,
			Window:              durationOrZero(cfg.AutoDrain.WindowMS),
			MinRequests:         cfg.AutoDrain.MinRequests,
			ErrorRateMultiplier: cfg.AutoDrain.ErrorRateMultiplier,
			Cooloff:             durationOrZero(cfg.AutoDrain.CooloffMS),
		},
	}, stablePool, canaryPool, nil
}

func stringOrDefault(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func sanitizeVaryHeaders(routeID string, values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if len(trimmed) > defaultCacheVaryHeaderMaxLen {
			return nil, fmt.Errorf("route %q cache vary header too long", routeID)
		}
		if !isASCII(trimmed) {
			return nil, fmt.Errorf("route %q cache vary header must be ASCII", routeID)
		}
		result = append(result, trimmed)
	}
	return result, nil
}

func boolOrDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func isASCII(value string) bool {
	for _, r := range value {
		if r > 127 {
			return false
		}
	}
	return true
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
