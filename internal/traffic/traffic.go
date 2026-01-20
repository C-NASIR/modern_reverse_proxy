package traffic

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

type Config struct {
	Enabled      bool
	StableWeight int
	CanaryWeight int
	Cohort       CohortConfig
	Overload     OverloadConfig
	AutoDrain    AutoDrainConfig
}

type CohortConfig struct {
	Enabled bool
	Key     string
}

type OverloadConfig struct {
	Enabled      bool
	MaxInflight  int
	MaxQueue     int
	QueueTimeout time.Duration
}

type Plan struct {
	Split     Split
	Cohort    *CohortExtractor
	Overload  *OverloadLimiter
	Stats     *Stats
	AutoDrain *AutoDrain
}

type PickMeta struct {
	CohortMode       string
	CohortKeyPresent bool
	AutoDrainActive  bool
}

func (p *Plan) PickVariant(r *http.Request) (Variant, PickMeta) {
	meta := PickMeta{CohortMode: "random"}
	if p == nil {
		return VariantStable, meta
	}
	split := p.Split
	if p.AutoDrain != nil && p.AutoDrain.Active() {
		meta.AutoDrainActive = true
		split = Split{StableWeight: p.Split.StableWeight, CanaryWeight: 0}
	}
	if p.Cohort != nil {
		key, ok := p.Cohort.Extract(r)
		meta.CohortKeyPresent = ok
		if ok {
			meta.CohortMode = "sticky"
			return split.ChooseDeterministic(key), meta
		}
	}
	return split.ChooseRandom(), meta
}

func (p *Plan) Stop() {
	if p == nil || p.AutoDrain == nil {
		return
	}
	p.AutoDrain.Stop()
}

type Registry struct {
	mu           sync.Mutex
	routes       map[string]*entry
	reapInterval time.Duration
	ttl          time.Duration
	stopCh       chan struct{}
}

type entry struct {
	config   Config
	plan     *Plan
	lastSeen time.Time
}

const (
	defaultReapInterval = 30 * time.Second
	defaultTTL          = 5 * time.Minute
)

func NewRegistry(reapInterval time.Duration, ttl time.Duration) *Registry {
	if reapInterval <= 0 {
		reapInterval = defaultReapInterval
	}
	if ttl <= 0 {
		ttl = defaultTTL
	}
	reg := &Registry{
		routes:       make(map[string]*entry),
		reapInterval: reapInterval,
		ttl:          ttl,
		stopCh:       make(chan struct{}),
	}
	go reg.reapLoop()
	return reg
}

func (r *Registry) Plan(routeID string, cfg Config) (*Plan, error) {
	if routeID == "" {
		return nil, errors.New("route id is empty")
	}
	if r == nil {
		return buildPlan(cfg)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.routes[routeID]
	if current != nil && sameConfig(current.config, cfg) {
		current.lastSeen = time.Now()
		return current.plan, nil
	}
	if current != nil && current.plan != nil {
		current.plan.Stop()
	}
	plan, err := buildPlan(cfg)
	if err != nil {
		return nil, err
	}
	r.routes[routeID] = &entry{config: cfg, plan: plan, lastSeen: time.Now()}
	return plan, nil
}

func (r *Registry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	for _, entry := range r.routes {
		if entry.plan != nil {
			entry.plan.Stop()
		}
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
	for id, entry := range r.routes {
		if entry.lastSeen.Before(cutoff) {
			if entry.plan != nil {
				entry.plan.Stop()
			}
			delete(r.routes, id)
		}
	}
	r.mu.Unlock()
}

func buildPlan(cfg Config) (*Plan, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	plan := &Plan{Split: Split{StableWeight: cfg.StableWeight, CanaryWeight: cfg.CanaryWeight}}
	if cfg.Cohort.Enabled {
		extractor, err := NewCohortExtractor(cfg.Cohort.Key)
		if err != nil {
			return nil, err
		}
		plan.Cohort = extractor
	}
	if cfg.Overload.Enabled {
		plan.Overload = NewOverloadLimiter(cfg.Overload.MaxInflight, cfg.Overload.MaxQueue, cfg.Overload.QueueTimeout)
	}
	plan.Stats = NewStats(cfg.AutoDrain.Window)
	if cfg.AutoDrain.Enabled {
		plan.AutoDrain = NewAutoDrain(plan.Stats, cfg.AutoDrain)
	}
	return plan, nil
}

func sameConfig(a Config, b Config) bool {
	return a.Enabled == b.Enabled &&
		a.StableWeight == b.StableWeight &&
		a.CanaryWeight == b.CanaryWeight &&
		cohortSame(a.Cohort, b.Cohort) &&
		overloadSame(a.Overload, b.Overload) &&
		autoDrainSame(a.AutoDrain, b.AutoDrain)
}

func cohortSame(a CohortConfig, b CohortConfig) bool {
	return a.Enabled == b.Enabled && a.Key == b.Key
}

func overloadSame(a OverloadConfig, b OverloadConfig) bool {
	return a.Enabled == b.Enabled && a.MaxInflight == b.MaxInflight && a.MaxQueue == b.MaxQueue && a.QueueTimeout == b.QueueTimeout
}

func autoDrainSame(a AutoDrainConfig, b AutoDrainConfig) bool {
	return a.Enabled == b.Enabled && a.Window == b.Window && a.MinRequests == b.MinRequests && a.ErrorRateMultiplier == b.ErrorRateMultiplier && a.Cooloff == b.Cooloff
}
