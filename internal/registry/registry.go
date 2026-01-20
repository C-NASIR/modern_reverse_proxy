package registry

import (
	"context"
	"net/http"
	"sync"
	"time"

	"modern_reverse_proxy/internal/health"
	"modern_reverse_proxy/internal/pool"
	"modern_reverse_proxy/internal/transport"
)

const (
	defaultReapInterval = 50 * time.Millisecond
	defaultDrainTimeout = 200 * time.Millisecond
)

type Registry struct {
	mu           sync.RWMutex
	pools        map[pool.PoolKey]*pool.PoolRuntime
	transports   *transport.Registry
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
		transports:   transport.NewRegistry(0, 0),
		reapInterval: reapInterval,
		drainTimeout: drainTimeout,
		stopCh:       make(chan struct{}),
	}
	go reg.reapLoop()
	return reg
}

func (r *Registry) Reconcile(key pool.PoolKey, endpoints []string, cfg health.Config, transportOpts transport.Options) {
	r.mu.Lock()
	poolRuntime := r.pools[key]
	if poolRuntime == nil {
		poolRuntime = pool.NewPoolRuntime(key, cfg, r.drainTimeout)
		r.pools[key] = poolRuntime
	}
	r.mu.Unlock()

	endpointRemoved := poolRuntime.Reconcile(endpoints, cfg, r.drainTimeout)
	if r.transports != nil {
		r.transports.Reconcile(string(key), endpoints, transportOpts)
		if endpointRemoved {
			r.transports.CloseIdleConnections(string(key))
		}
	}
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
	if r.transports != nil {
		r.transports.Stop()
	}
}

func (r *Registry) Stop(ctx context.Context) error {
	_ = ctx
	if r == nil {
		return nil
	}
	r.Close()
	if r.transports != nil {
		r.transports.CloseAll()
	}
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

func (r *Registry) Transport(poolKey pool.PoolKey) *http.Transport {
	if r == nil || r.transports == nil {
		return nil
	}
	return r.transports.Get(string(poolKey))
}

func (r *Registry) HasTransport(poolKey pool.PoolKey) bool {
	if r == nil || r.transports == nil {
		return false
	}
	return r.transports.Has(string(poolKey))
}

func (r *Registry) CloseIdleConnections() {
	if r == nil || r.transports == nil {
		return
	}
	r.transports.CloseIdleConnectionsAll()
}

func (r *Registry) SetTransportTTL(ttl time.Duration) {
	if r == nil || r.transports == nil {
		return
	}
	r.transports.SetTTL(ttl)
}

func (r *Registry) ReapTransportsNow() {
	if r == nil || r.transports == nil {
		return
	}
	r.transports.ReapNow()
}

func (r *Registry) PrunePools(desired map[pool.PoolKey]struct{}) {
	if r == nil {
		return
	}
	if desired == nil {
		desired = map[pool.PoolKey]struct{}{}
	}

	var removed []*pool.PoolRuntime
	var removedKeys []pool.PoolKey

	r.mu.Lock()
	for key, poolRuntime := range r.pools {
		if _, ok := desired[key]; ok {
			continue
		}
		removed = append(removed, poolRuntime)
		removedKeys = append(removedKeys, key)
		delete(r.pools, key)
	}
	r.mu.Unlock()

	for _, poolRuntime := range removed {
		poolRuntime.Stop()
	}
	if r.transports != nil {
		for _, key := range removedKeys {
			r.transports.Remove(string(key))
		}
	}
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
