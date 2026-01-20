package registry

import (
	"context"
	"sync"
	"time"

	"modern_reverse_proxy/internal/health"
	"modern_reverse_proxy/internal/pool"
)

const (
	defaultReapInterval = 50 * time.Millisecond
	defaultDrainTimeout = 200 * time.Millisecond
)

type Registry struct {
	mu           sync.RWMutex
	pools        map[pool.PoolKey]*pool.PoolRuntime
	reapInterval time.Duration
	drainTimeout time.Duration
	stopCh       chan struct{}
}

func NewRegistry(reapInterval time.Duration, drainTimeout time.Duration) *Registry {
	if reapInterval <= 0 {
		reapInterval = defaultReapInterval
	}
	if drainTimeout <= 0 {
		drainTimeout = defaultDrainTimeout
	}

	reg := &Registry{
		pools:        make(map[pool.PoolKey]*pool.PoolRuntime),
		reapInterval: reapInterval,
		drainTimeout: drainTimeout,
		stopCh:       make(chan struct{}),
	}
	go reg.reapLoop()
	return reg
}

func (r *Registry) Reconcile(key pool.PoolKey, endpoints []string, cfg health.Config) {
	r.mu.Lock()
	poolRuntime := r.pools[key]
	if poolRuntime == nil {
		poolRuntime = pool.NewPoolRuntime(key, cfg, r.drainTimeout)
		r.pools[key] = poolRuntime
	}
	r.mu.Unlock()

	poolRuntime.Reconcile(endpoints, cfg, r.drainTimeout)
}

func (r *Registry) Pick(key pool.PoolKey, outlierEjected func(addr string, now time.Time) bool) (pool.PickResult, bool) {
	poolRuntime := r.getPool(key)
	if poolRuntime == nil {
		return pool.PickResult{}, false
	}
	return poolRuntime.Pick(outlierEjected), true
}

func (r *Registry) InflightStart(key pool.PoolKey, addr string) {
	if endpoint := r.endpoint(key, addr); endpoint != nil {
		endpoint.InflightInc()
	}
}

func (r *Registry) InflightDone(key pool.PoolKey, addr string) {
	if endpoint := r.endpoint(key, addr); endpoint != nil {
		endpoint.InflightDec()
	}
}

func (r *Registry) PassiveFailure(key pool.PoolKey, addr string) {
	if endpoint := r.endpoint(key, addr); endpoint != nil {
		endpoint.RecordPassiveFailure()
	}
}

func (r *Registry) PassiveSuccess(key pool.PoolKey, addr string) {
	if endpoint := r.endpoint(key, addr); endpoint != nil {
		endpoint.RecordPassiveSuccess()
	}
}

func (r *Registry) HasEndpoint(key pool.PoolKey, addr string) bool {
	return r.endpoint(key, addr) != nil
}

func (r *Registry) Close() {
	select {
	case <-r.stopCh:
		return
	default:
		close(r.stopCh)
	}
}

func (r *Registry) Stop(ctx context.Context) error {
	_ = ctx
	if r == nil {
		return nil
	}
	r.Close()
	r.mu.RLock()
	pools := make([]*pool.PoolRuntime, 0, len(r.pools))
	for _, poolRuntime := range r.pools {
		pools = append(pools, poolRuntime)
	}
	r.mu.RUnlock()

	for _, poolRuntime := range pools {
		poolRuntime.Stop()
	}
	return nil
}

func (r *Registry) getPool(key pool.PoolKey) *pool.PoolRuntime {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pools[key]
}

func (r *Registry) endpoint(key pool.PoolKey, addr string) *pool.EndpointRuntime {
	poolRuntime := r.getPool(key)
	if poolRuntime == nil {
		return nil
	}
	return poolRuntime.Endpoint(addr)
}

func (r *Registry) reapLoop() {
	ticker := time.NewTicker(r.reapInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.reapOnce()
		case <-r.stopCh:
			return
		}
	}
}

func (r *Registry) reapOnce() {
	r.mu.RLock()
	pools := make([]*pool.PoolRuntime, 0, len(r.pools))
	for _, poolRuntime := range r.pools {
		pools = append(pools, poolRuntime)
	}
	r.mu.RUnlock()

	now := time.Now()
	for _, poolRuntime := range pools {
		poolRuntime.Reap(now)
	}
}
