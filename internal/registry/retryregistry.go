package registry

import (
	"errors"
	"sync"
	"time"

	"modern_reverse_proxy/internal/policy"
	"modern_reverse_proxy/internal/retry"
)

const (
	defaultRetryReapInterval = 30 * time.Second
	defaultRetryTTL          = 5 * time.Minute
)

type RetryRegistry struct {
	mu           sync.Mutex
	routes       map[string]*retryRouteEntry
	reapInterval time.Duration
	ttl          time.Duration
	stopCh       chan struct{}
}

type retryRouteEntry struct {
	budgetPolicy policy.RetryBudgetPolicy
	clientPolicy policy.ClientRetryCapPolicy
	budget       *retry.Budget
	clientCap    *retry.ClientCap
	lastUsed     time.Time
}

func NewRetryRegistry(reapInterval time.Duration, ttl time.Duration) *RetryRegistry {
	if reapInterval <= 0 {
		reapInterval = defaultRetryReapInterval
	}
	if ttl <= 0 {
		ttl = defaultRetryTTL
	}

	registry := &RetryRegistry{
		routes:       make(map[string]*retryRouteEntry),
		reapInterval: reapInterval,
		ttl:          ttl,
		stopCh:       make(chan struct{}),
	}
	go registry.reapLoop()
	return registry
}

func (r *RetryRegistry) Budgets(routeID string, budgetPolicy policy.RetryBudgetPolicy, clientPolicy policy.ClientRetryCapPolicy) (*retry.Budget, *retry.ClientCap, error) {
	if r == nil {
		return nil, nil, errors.New("retry registry is nil")
	}
	if routeID == "" {
		return nil, nil, errors.New("route id is empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	entry := r.routes[routeID]
	if entry == nil || !sameRetryBudgetPolicy(entry.budgetPolicy, budgetPolicy) || !sameClientPolicy(entry.clientPolicy, clientPolicy) {
		entry = &retryRouteEntry{
			budgetPolicy: budgetPolicy,
			clientPolicy: clientPolicy,
			budget:       nil,
			clientCap:    nil,
			lastUsed:     time.Now(),
		}
		if budgetPolicy.Enabled {
			entry.budget = retry.NewBudget(budgetPolicy.PercentOfSuccesses, budgetPolicy.Burst)
		}
		if clientPolicy.Enabled {
			if clientPolicy.LRUSize <= 0 {
				return nil, nil, errors.New("client cap lru size must be positive")
			}
			entry.clientCap = retry.NewClientCap(clientPolicy.PercentOfSuccesses, clientPolicy.Burst, clientPolicy.LRUSize)
		}
		r.routes[routeID] = entry
	}
	entry.lastUsed = time.Now()

	return entry.budget, entry.clientCap, nil
}

func (r *RetryRegistry) Close() {
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

func (r *RetryRegistry) reapLoop() {
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

func (r *RetryRegistry) reapOnce() {
	if r == nil {
		return
	}
	cutoff := time.Now().Add(-r.ttl)

	r.mu.Lock()
	for routeID, entry := range r.routes {
		if entry.lastUsed.Before(cutoff) {
			delete(r.routes, routeID)
		}
	}
	r.mu.Unlock()
}

func sameRetryBudgetPolicy(a policy.RetryBudgetPolicy, b policy.RetryBudgetPolicy) bool {
	return a.Enabled == b.Enabled && a.PercentOfSuccesses == b.PercentOfSuccesses && a.Burst == b.Burst
}

func sameClientPolicy(a policy.ClientRetryCapPolicy, b policy.ClientRetryCapPolicy) bool {
	return a.Enabled == b.Enabled && a.Key == b.Key && a.PercentOfSuccesses == b.PercentOfSuccesses && a.Burst == b.Burst && a.LRUSize == b.LRUSize
}
