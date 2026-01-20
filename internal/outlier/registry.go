package outlier

import (
	"context"
	"sync"
	"time"
)

const (
	defaultReapInterval = 30 * time.Second
	defaultTTL          = 30 * time.Minute
)

type EjectionObserver func(poolKey string, reason string)

type Registry struct {
	mu           sync.Mutex
	pools        map[string]*poolEntry
	reapInterval time.Duration
	ttl          time.Duration
	stopCh       chan struct{}
	observer     EjectionObserver
}

type poolEntry struct {
	key             string
	config          Config
	endpoints       map[string]*EndpointState
	lastSeen        time.Time
	latencyStopCh   chan struct{}
	latencyInterval time.Duration
}

func NewRegistry(reapInterval time.Duration, ttl time.Duration, observer EjectionObserver) *Registry {
	if reapInterval <= 0 {
		reapInterval = defaultReapInterval
	}
	if ttl <= 0 {
		ttl = defaultTTL
	}

	registry := &Registry{
		pools:        make(map[string]*poolEntry),
		reapInterval: reapInterval,
		ttl:          ttl,
		stopCh:       make(chan struct{}),
		observer:     observer,
	}
	go registry.reapLoop()
	return registry
}

func (r *Registry) Reconcile(poolKey string, endpoints []string, cfg Config) {
	if r == nil || poolKey == "" {
		return
	}

	r.mu.Lock()
	entry := r.pools[poolKey]
	if entry == nil {
		entry = &poolEntry{key: poolKey, endpoints: make(map[string]*EndpointState)}
		r.pools[poolKey] = entry
	}
	entry.lastSeen = time.Now()
	entry.config = cfg

	desired := make(map[string]struct{}, len(endpoints))
	for _, addr := range endpoints {
		desired[addr] = struct{}{}
		state := entry.endpoints[addr]
		if state == nil {
			entry.endpoints[addr] = NewEndpointState(cfg)
		} else {
			state.UpdateConfig(cfg)
		}
	}
	for addr := range entry.endpoints {
		if _, ok := desired[addr]; !ok {
			delete(entry.endpoints, addr)
		}
	}

	if cfg.Enabled && cfg.LatencyEnabled {
		interval := cfg.LatencyEvalInterval
		if interval <= 0 {
			interval = time.Second
		}
		if entry.latencyStopCh == nil || entry.latencyInterval != interval {
			entry.stopLatencyLoopLocked()
			entry.startLatencyLoopLocked(r, interval)
		}
	} else {
		entry.stopLatencyLoopLocked()
	}
	r.mu.Unlock()
}

func (r *Registry) RecordResult(poolKey string, addr string, success bool, latency time.Duration) (bool, string) {
	if r == nil || poolKey == "" || addr == "" {
		return false, ""
	}
	config, endpoint := r.endpoint(poolKey, addr)
	if endpoint == nil {
		return false, ""
	}
	if !config.Enabled {
		return false, ""
	}

	now := time.Now()
	if success {
		endpoint.RecordLatency(latency)
	}
	ejected, reason := endpoint.RecordResult(config, success, now)
	if ejected {
		r.notify(poolKey, reason)
		return true, reason
	}
	return false, ""
}

func (r *Registry) IsEjected(poolKey string, addr string, now time.Time) bool {
	if r == nil || poolKey == "" || addr == "" {
		return false
	}
	config, endpoint := r.endpoint(poolKey, addr)
	if endpoint == nil {
		return false
	}
	if !config.Enabled {
		return false
	}
	return endpoint.IsEjected(now)
}

func (r *Registry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	for _, entry := range r.pools {
		entry.stopLatencyLoopLocked()
	}
	r.mu.Unlock()
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
	return nil
}

func (r *Registry) HasPool(key string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.pools[key]
	return ok
}

func (r *Registry) endpoint(poolKey string, addr string) (Config, *EndpointState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.pools[poolKey]
	if entry == nil {
		return Config{}, nil
	}
	entry.lastSeen = time.Now()
	return entry.config, entry.endpoints[addr]
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
	if r == nil {
		return
	}
	cutoff := time.Now().Add(-r.ttl)

	r.mu.Lock()
	for key, entry := range r.pools {
		if entry.lastSeen.Before(cutoff) {
			entry.stopLatencyLoopLocked()
			delete(r.pools, key)
		}
	}
	r.mu.Unlock()
}

func (r *Registry) notify(poolKey string, reason string) {
	if r == nil || r.observer == nil || reason == "" {
		return
	}
	r.observer(poolKey, reason)
}

func (e *poolEntry) startLatencyLoopLocked(r *Registry, interval time.Duration) {
	stopCh := make(chan struct{})
	e.latencyStopCh = stopCh
	e.latencyInterval = interval
	poolKey := e.key
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				r.evaluateLatency(poolKey)
			case <-stopCh:
				return
			}
		}
	}()
}

func (e *poolEntry) stopLatencyLoopLocked() {
	if e.latencyStopCh == nil {
		return
	}
	close(e.latencyStopCh)
	e.latencyStopCh = nil
	e.latencyInterval = 0
}

func (r *Registry) evaluateLatency(poolKey string) {
	defer func() {
		_ = recover()
	}()

	r.mu.Lock()
	entry := r.pools[poolKey]
	if entry == nil {
		r.mu.Unlock()
		return
	}
	config := entry.config
	endpoints := make(map[string]*EndpointState, len(entry.endpoints))
	for addr, state := range entry.endpoints {
		endpoints[addr] = state
	}
	r.mu.Unlock()

	if !config.Enabled || !config.LatencyEnabled {
		return
	}

	minSamples := config.LatencyMinSamples
	if minSamples <= 0 {
		minSamples = 1
	}
	var baselineSamples []int64
	endpointSamples := make(map[*EndpointState][]int64)
	for _, endpoint := range endpoints {
		samples := endpoint.LatencySnapshot()
		if len(samples) < minSamples {
			continue
		}
		endpointSamples[endpoint] = samples
		baselineSamples = append(baselineSamples, samples...)
	}
	if len(baselineSamples) < minSamples {
		return
	}
	baseline := percentile(baselineSamples, 0.50)
	if baseline <= 0 {
		return
	}

	now := time.Now()
	multiplier := config.LatencyMultiplier
	if multiplier <= 0 {
		multiplier = 1
	}
	for endpoint, samples := range endpointSamples {
		p95 := percentile(samples, 0.95)
		threshold := int64(multiplier) * baseline
		bad := p95 > threshold
		if ejected, reason := endpoint.ObserveLatency(config, bad, now); ejected {
			r.notify(poolKey, reason)
		}
	}
}
