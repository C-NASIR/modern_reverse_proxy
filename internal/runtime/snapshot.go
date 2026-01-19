package runtime

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/health"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/policy"
	"modern_reverse_proxy/internal/pool"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/router"
)

type Snapshot struct {
	Router    *router.Router
	Pools     map[string]pool.PoolKey
	Version   string
	CreatedAt time.Time
	Source    string
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
)

var (
	defaultRetryStatuses = []int{502, 503, 504}
	defaultRetryErrors   = []string{"dial", "timeout"}
)

func BuildSnapshot(cfg *config.Config, reg *registry.Registry) (*Snapshot, error) {
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
	}

	seenIDs := make(map[string]struct{}, len(cfg.Routes))
	routes := make([]policy.Route, 0, len(cfg.Routes))
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
		}

		routes = append(routes, policy.Route{
			ID:         route.ID,
			Host:       route.Host,
			PathPrefix: route.PathPrefix,
			Methods:    methods,
			PoolName:   route.Pool,
			Policy:     policyRuntime,
		})
	}

	compiled := router.NewRouter(routes)
	if compiled == nil {
		return nil, errors.New("router build failed")
	}

	snapshot := &Snapshot{
		Router:    compiled,
		Pools:     pools,
		Version:   fmt.Sprintf("v-%d", time.Now().UnixNano()),
		CreatedAt: time.Now().UTC(),
		Source:    "file",
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
