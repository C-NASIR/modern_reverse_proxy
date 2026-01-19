package pool

import (
	"sync"
	"sync/atomic"
	"time"

	"modern_reverse_proxy/internal/health"
)

type PoolKey string

type EndpointKey struct {
	Pool PoolKey
	Addr string
}

const (
	stateHealthy int32 = iota + 1
	stateUnhealthy
	stateDraining
)

type PoolRuntime struct {
	key          PoolKey
	healthConfig health.Config
	endpoints    map[string]*EndpointRuntime
	order        []string
	rr           uint64
	mu           sync.RWMutex
	drainTimeout time.Duration
}

type PickResult struct {
	Addr             string
	SelectedHealthy  bool
	SelectedFailOpen bool
	OutlierIgnored   bool
	EndpointEjected  bool
}

func NewPoolRuntime(key PoolKey, cfg health.Config, drainTimeout time.Duration) *PoolRuntime {
	return &PoolRuntime{
		key:          key,
		healthConfig: cfg,
		endpoints:    make(map[string]*EndpointRuntime),
		drainTimeout: drainTimeout,
	}
}

func (p *PoolRuntime) Reconcile(endpoints []string, cfg health.Config, drainTimeout time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.healthConfig = cfg
	p.drainTimeout = drainTimeout
	p.order = append(p.order[:0], endpoints...)
	desired := make(map[string]struct{}, len(endpoints))

	for _, addr := range endpoints {
		desired[addr] = struct{}{}
		if endpoint, ok := p.endpoints[addr]; ok {
			endpoint.UpdateConfig(cfg)
			endpoint.Restore()
			continue
		}
		p.endpoints[addr] = NewEndpointRuntime(addr, cfg)
	}

	for addr, endpoint := range p.endpoints {
		if _, ok := desired[addr]; ok {
			continue
		}
		endpoint.MarkDraining(drainTimeout)
	}
}

func (p *PoolRuntime) Pick(outlierEjected func(addr string, now time.Time) bool) PickResult {
	p.mu.RLock()
	if len(p.endpoints) == 0 {
		p.mu.RUnlock()
		return PickResult{}
	}

	all := make([]*EndpointRuntime, 0, len(p.endpoints))
	for _, addr := range p.order {
		if endpoint := p.endpoints[addr]; endpoint != nil {
			all = append(all, endpoint)
		}
	}
	if len(all) == 0 {
		for _, endpoint := range p.endpoints {
			all = append(all, endpoint)
		}
	}
	p.mu.RUnlock()

	now := time.Now()
	eligible := make([]*EndpointRuntime, 0, len(all))
	nonDraining := make([]*EndpointRuntime, 0, len(all))
	outlierSuppressed := false

	for _, endpoint := range all {
		isOutlierEjected := false
		if outlierEjected != nil {
			isOutlierEjected = outlierEjected(endpoint.addr, now)
		}
		if !endpoint.IsDraining() {
			nonDraining = append(nonDraining, endpoint)
		}
		isHealthy := endpoint.IsHealthy() && !endpoint.IsEjected(now)
		if endpoint.IsHealthy() && !endpoint.IsDraining() && !endpoint.IsEjected(now) && !isOutlierEjected {
			eligible = append(eligible, endpoint)
		} else if isHealthy && !endpoint.IsDraining() && isOutlierEjected {
			outlierSuppressed = true
		}
	}

	if len(eligible) > 0 {
		picked := p.pickFrom(eligible)
		return PickResult{
			Addr:            picked.addr,
			SelectedHealthy: true,
		}
	}
	if len(nonDraining) > 0 {
		picked := p.pickFrom(nonDraining)
		endpointEjected := !picked.IsHealthy() || picked.IsEjected(now)
		if outlierEjected != nil {
			endpointEjected = endpointEjected || outlierEjected(picked.addr, now)
		}
		return PickResult{
			Addr:             picked.addr,
			SelectedFailOpen: true,
			OutlierIgnored:   outlierSuppressed,
			EndpointEjected:  endpointEjected,
		}
	}
	picked := p.pickFrom(all)
	endpointEjected := !picked.IsHealthy() || picked.IsEjected(now)
	if outlierEjected != nil {
		endpointEjected = endpointEjected || outlierEjected(picked.addr, now)
	}
	return PickResult{
		Addr:             picked.addr,
		SelectedFailOpen: true,
		OutlierIgnored:   outlierSuppressed,
		EndpointEjected:  endpointEjected,
	}
}

func (p *PoolRuntime) pickFrom(endpoints []*EndpointRuntime) *EndpointRuntime {
	idx := atomic.AddUint64(&p.rr, 1) - 1
	endpoint := endpoints[idx%uint64(len(endpoints))]
	endpoint.MarkSeen()
	return endpoint
}

func (p *PoolRuntime) Endpoint(addr string) *EndpointRuntime {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.endpoints[addr]
}

func (p *PoolRuntime) Reap(now time.Time) []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	removed := []string{}
	for addr, endpoint := range p.endpoints {
		if endpoint.IsDraining() && endpoint.DrainDeadlineReached(now) && endpoint.Inflight() == 0 {
			endpoint.Stop()
			delete(p.endpoints, addr)
			removed = append(removed, addr)
		}
	}
	return removed
}

type EndpointRuntime struct {
	addr                     string
	state                    atomic.Int32
	ejectUntil               atomic.Int64
	consecutiveActiveFails   atomic.Int32
	consecutiveActiveSuccess atomic.Int32
	consecutivePassiveFails  atomic.Int32
	inflight                 atomic.Int64
	ejectCount               atomic.Int32
	lastHealthyAt            atomic.Int64
	lastSeen                 atomic.Int64
	drainUntil               atomic.Int64
	config                   atomic.Value
	activeMu                 sync.Mutex
	stopCh                   chan struct{}
	stopOnce                 sync.Once
}

func NewEndpointRuntime(addr string, cfg health.Config) *EndpointRuntime {
	endpoint := &EndpointRuntime{addr: addr}
	endpoint.state.Store(stateHealthy)
	endpoint.config.Store(cfg)
	endpoint.startActive(cfg)
	return endpoint
}

func (e *EndpointRuntime) UpdateConfig(cfg health.Config) {
	e.config.Store(cfg)
	e.restartActive(cfg)
}

func (e *EndpointRuntime) Restore() {
	if e.state.Load() == stateDraining {
		e.state.Store(stateHealthy)
		e.drainUntil.Store(0)
		cfg := e.config.Load().(health.Config)
		e.restartActive(cfg)
	}
}

func (e *EndpointRuntime) Address() string {
	return e.addr
}

func (e *EndpointRuntime) MarkDraining(timeout time.Duration) {
	if e.state.Load() == stateDraining {
		return
	}
	e.state.Store(stateDraining)
	e.drainUntil.Store(time.Now().Add(timeout).UnixNano())
	e.stopActive()
}

func (e *EndpointRuntime) IsDraining() bool {
	return e.state.Load() == stateDraining
}

func (e *EndpointRuntime) DrainDeadlineReached(now time.Time) bool {
	deadline := e.drainUntil.Load()
	return deadline > 0 && now.UnixNano() >= deadline
}

func (e *EndpointRuntime) IsHealthy() bool {
	return e.state.Load() == stateHealthy
}

func (e *EndpointRuntime) IsEjected(now time.Time) bool {
	until := e.ejectUntil.Load()
	return until > 0 && now.UnixNano() < until
}

func (e *EndpointRuntime) MarkSeen() {
	e.lastSeen.Store(time.Now().UnixNano())
}

func (e *EndpointRuntime) RecordActiveSuccess() {
	if e.IsDraining() {
		return
	}
	cfg := e.config.Load().(health.Config)
	e.consecutiveActiveSuccess.Add(1)
	e.consecutiveActiveFails.Store(0)
	if cfg.HealthyAfterSuccesses > 0 && int(e.consecutiveActiveSuccess.Load()) >= cfg.HealthyAfterSuccesses {
		e.markHealthy()
	}
}

func (e *EndpointRuntime) RecordActiveFailure() {
	if e.IsDraining() {
		return
	}
	cfg := e.config.Load().(health.Config)
	e.consecutiveActiveFails.Add(1)
	e.consecutiveActiveSuccess.Store(0)
	if cfg.UnhealthyAfterFailures > 0 && int(e.consecutiveActiveFails.Load()) >= cfg.UnhealthyAfterFailures {
		e.eject(cfg)
	}
}

func (e *EndpointRuntime) RecordPassiveSuccess() {
	e.consecutivePassiveFails.Store(0)
}

func (e *EndpointRuntime) RecordPassiveFailure() {
	if e.IsDraining() {
		return
	}
	cfg := e.config.Load().(health.Config)
	e.consecutivePassiveFails.Add(1)
	if cfg.UnhealthyAfterFailures > 0 && int(e.consecutivePassiveFails.Load()) >= cfg.UnhealthyAfterFailures {
		e.eject(cfg)
	}
}

func (e *EndpointRuntime) InflightInc() {
	e.inflight.Add(1)
}

func (e *EndpointRuntime) InflightDec() {
	e.inflight.Add(-1)
}

func (e *EndpointRuntime) Inflight() int64 {
	return e.inflight.Load()
}

func (e *EndpointRuntime) Stop() {
	e.stopActive()
}

func (e *EndpointRuntime) markHealthy() {
	e.state.Store(stateHealthy)
	e.ejectUntil.Store(0)
	e.consecutivePassiveFails.Store(0)
	e.consecutiveActiveFails.Store(0)
	e.lastHealthyAt.Store(time.Now().UnixNano())
}

func (e *EndpointRuntime) eject(cfg health.Config) {
	e.state.Store(stateUnhealthy)
	now := time.Now()
	base := cfg.BaseEjectDuration
	max := cfg.MaxEjectDuration
	if base <= 0 {
		base = time.Second
	}
	if max <= 0 {
		max = base
	}

	lastHealthy := e.lastHealthyAt.Load()
	if lastHealthy > 0 && now.Sub(time.Unix(0, lastHealthy)) > 5*time.Minute {
		e.ejectCount.Store(0)
	}

	count := e.ejectCount.Add(1)
	duration := base
	for i := int32(1); i < count; i++ {
		duration *= 2
		if duration >= max {
			duration = max
			break
		}
	}

	if duration > max {
		duration = max
	}
	e.ejectUntil.Store(now.Add(duration).UnixNano())
}

func (e *EndpointRuntime) restartActive(cfg health.Config) {
	e.activeMu.Lock()
	defer e.activeMu.Unlock()
	e.stopActiveLocked()
	e.startActiveLocked(cfg)
}

func (e *EndpointRuntime) startActive(cfg health.Config) {
	e.activeMu.Lock()
	defer e.activeMu.Unlock()
	e.startActiveLocked(cfg)
}

func (e *EndpointRuntime) startActiveLocked(cfg health.Config) {
	if cfg.Interval <= 0 {
		return
	}
	if e.stopCh != nil {
		return
	}
	e.stopCh = make(chan struct{})
	e.stopOnce = sync.Once{}
	go health.ActiveProbeLoop(cfg, e.addr, e.stopCh, e.RecordActiveSuccess, e.RecordActiveFailure)
}

func (e *EndpointRuntime) stopActive() {
	e.activeMu.Lock()
	defer e.activeMu.Unlock()
	e.stopActiveLocked()
}

func (e *EndpointRuntime) stopActiveLocked() {
	if e.stopCh == nil {
		return
	}
	stopCh := e.stopCh
	e.stopOnce.Do(func() {
		close(stopCh)
	})
	e.stopCh = nil
}
