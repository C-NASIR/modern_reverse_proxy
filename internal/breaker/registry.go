package breaker

import (
	"errors"
	"sync"
	"time"
)

const (
	defaultReapInterval = 30 * time.Second
	defaultTTL          = 30 * time.Minute
)

type Registry struct {
	mu           sync.Mutex
	breakers     map[string]*entry
	reapInterval time.Duration
	ttl          time.Duration
	stopCh       chan struct{}
}

type entry struct {
	breaker  *Breaker
	config   Config
	lastSeen time.Time
}

func NewRegistry(reapInterval time.Duration, ttl time.Duration) *Registry {
	if reapInterval <= 0 {
		reapInterval = defaultReapInterval
	}
	if ttl <= 0 {
		ttl = defaultTTL
	}

	registry := &Registry{
		breakers:     make(map[string]*entry),
		reapInterval: reapInterval,
		ttl:          ttl,
		stopCh:       make(chan struct{}),
	}
	go registry.reapLoop()
	return registry
}

func (r *Registry) Allow(key string, cfg Config) (State, bool, error) {
	if r == nil {
		return StateClosed, true, errors.New("breaker registry is nil")
	}
	if key == "" {
		return StateClosed, true, errors.New("breaker key is empty")
	}
	entry := r.ensure(key, cfg)
	return entry.breaker.Allow()
}

func (r *Registry) Report(key string, cfg Config, success bool) (State, error) {
	if r == nil {
		return StateClosed, errors.New("breaker registry is nil")
	}
	if key == "" {
		return StateClosed, errors.New("breaker key is empty")
	}
	entry := r.ensure(key, cfg)
	return entry.breaker.Report(success)
}

func (r *Registry) Close() {
	if r == nil {
		return
	}
	select {
	case <-r.stopCh:
		return
	default:
		close(r.stopCh)
	}
}

func (r *Registry) Has(key string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.breakers[key]
	return ok
}

func (r *Registry) ensure(key string, cfg Config) *entry {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.breakers[key]
	if current == nil {
		current = &entry{breaker: New(cfg), config: cfg, lastSeen: time.Now()}
		r.breakers[key] = current
		return current
	}
	current.lastSeen = time.Now()
	if !sameConfig(current.config, cfg) {
		current.config = cfg
		current.breaker.UpdateConfig(cfg)
	}
	return current
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
	for key, entry := range r.breakers {
		if entry.lastSeen.Before(cutoff) {
			delete(r.breakers, key)
		}
	}
	r.mu.Unlock()
}

func sameConfig(a Config, b Config) bool {
	return a.Enabled == b.Enabled &&
		a.FailureRateThresholdPercent == b.FailureRateThresholdPercent &&
		a.MinimumRequests == b.MinimumRequests &&
		a.EvaluationWindow == b.EvaluationWindow &&
		a.OpenDuration == b.OpenDuration &&
		a.HalfOpenMaxProbes == b.HalfOpenMaxProbes
}
