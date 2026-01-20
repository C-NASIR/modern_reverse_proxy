package transport

import (
	"log"
	"net/http"
	"sync"
	"time"
)

const (
	defaultTransportTTL          = 10 * time.Minute
	defaultTransportReapInterval = time.Minute
)

type Registry struct {
	mu           sync.Mutex
	transports   map[string]*transportEntry
	defaultOpts  Options
	ttl          time.Duration
	reapInterval time.Duration
	stopCh       chan struct{}
}

type transportEntry struct {
	transport      *http.Transport
	opts           Options
	lastUsed       time.Time
	lastReconciled time.Time
	draining       bool
	removedAt      time.Time
}

func NewRegistry(ttl time.Duration, reapInterval time.Duration) *Registry {
	if ttl <= 0 {
		ttl = defaultTransportTTL
	}
	if reapInterval <= 0 {
		reapInterval = defaultTransportReapInterval
	}

	r := &Registry{
		transports:   make(map[string]*transportEntry),
		defaultOpts:  DefaultOptions(),
		ttl:          ttl,
		reapInterval: reapInterval,
		stopCh:       make(chan struct{}),
	}
	go r.reapLoop()
	return r
}

func (r *Registry) Get(poolKey string) *http.Transport {
	if r == nil {
		return nil
	}
	if poolKey == "" {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.transports[poolKey]
	if current == nil {
		transport := NewTransport(r.defaultOpts)
		r.transports[poolKey] = &transportEntry{
			transport:      transport,
			opts:           r.defaultOpts,
			lastUsed:       time.Now(),
			lastReconciled: time.Now(),
		}
		log.Printf("transport registry missing pool %s; created default transport", poolKey)
		return transport
	}

	current.lastUsed = time.Now()
	return current.transport
}

func (r *Registry) Reconcile(poolKey string, _ []string, opts Options) *http.Transport {
	if r == nil {
		return nil
	}
	if poolKey == "" {
		return nil
	}

	opts = mergeOptions(r.defaultOpts, opts)
	var old *http.Transport

	r.mu.Lock()
	current := r.transports[poolKey]
	if current == nil {
		transport := NewTransport(opts)
		r.transports[poolKey] = &transportEntry{
			transport:      transport,
			opts:           opts,
			lastUsed:       time.Now(),
			lastReconciled: time.Now(),
		}
		r.mu.Unlock()
		return transport
	}

	if !optionsEqual(current.opts, opts) {
		old = current.transport
		current.transport = NewTransport(opts)
		current.opts = opts
	}
	current.lastReconciled = time.Now()
	current.draining = false
	current.removedAt = time.Time{}
	transport := current.transport
	r.mu.Unlock()

	if old != nil {
		safeCloseIdle(old)
	}
	return transport
}

func (r *Registry) Remove(poolKey string) {
	if r == nil || poolKey == "" {
		return
	}

	r.mu.Lock()
	current := r.transports[poolKey]
	if current == nil {
		r.mu.Unlock()
		return
	}
	current.draining = true
	current.removedAt = time.Now()
	transport := current.transport
	r.mu.Unlock()

	safeCloseIdle(transport)
}

func (r *Registry) CloseIdleConnections(poolKey string) {
	if r == nil || poolKey == "" {
		return
	}
	r.mu.Lock()
	current := r.transports[poolKey]
	transport := (*http.Transport)(nil)
	if current != nil {
		transport = current.transport
	}
	r.mu.Unlock()
	safeCloseIdle(transport)
}

func (r *Registry) CloseIdleConnectionsAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	entries := make([]*http.Transport, 0, len(r.transports))
	for _, current := range r.transports {
		entries = append(entries, current.transport)
	}
	r.mu.Unlock()

	for _, transport := range entries {
		safeCloseIdle(transport)
	}
}

func (r *Registry) CloseAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	entries := make([]*http.Transport, 0, len(r.transports))
	for _, current := range r.transports {
		entries = append(entries, current.transport)
	}
	r.transports = make(map[string]*transportEntry)
	r.mu.Unlock()

	for _, transport := range entries {
		safeCloseIdle(transport)
	}
}

func (r *Registry) Has(poolKey string) bool {
	if r == nil || poolKey == "" {
		return false
	}
	r.mu.Lock()
	_, ok := r.transports[poolKey]
	r.mu.Unlock()
	return ok
}

func (r *Registry) SetTTL(ttl time.Duration) {
	if r == nil {
		return
	}
	if ttl <= 0 {
		ttl = defaultTransportTTL
	}
	r.mu.Lock()
	r.ttl = ttl
	r.mu.Unlock()
}

func (r *Registry) Stop() {
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

func (r *Registry) ReapNow() {
	if r == nil {
		return
	}
	r.reapOnce()
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
	now := time.Now()
	var expired []*http.Transport

	r.mu.Lock()
	for key, current := range r.transports {
		if !current.draining {
			continue
		}
		if r.ttl > 0 && now.Sub(current.removedAt) < r.ttl {
			continue
		}
		expired = append(expired, current.transport)
		delete(r.transports, key)
	}
	r.mu.Unlock()

	for _, transport := range expired {
		safeCloseIdle(transport)
	}
}

func mergeOptions(defaults Options, override Options) Options {
	if override.DialTimeout > 0 {
		defaults.DialTimeout = override.DialTimeout
	}
	if override.TLSHandshakeTimeout > 0 {
		defaults.TLSHandshakeTimeout = override.TLSHandshakeTimeout
	}
	if override.ResponseHeaderTimeout > 0 {
		defaults.ResponseHeaderTimeout = override.ResponseHeaderTimeout
	}
	if override.ExpectContinueTimeout > 0 {
		defaults.ExpectContinueTimeout = override.ExpectContinueTimeout
	}
	if override.IdleConnTimeout > 0 {
		defaults.IdleConnTimeout = override.IdleConnTimeout
	}
	if override.MaxIdleConns > 0 {
		defaults.MaxIdleConns = override.MaxIdleConns
	}
	if override.MaxIdleConnsPerHost > 0 {
		defaults.MaxIdleConnsPerHost = override.MaxIdleConnsPerHost
	}
	if override.MaxConnsPerHost >= 0 {
		defaults.MaxConnsPerHost = override.MaxConnsPerHost
	}
	return defaults
}

func optionsEqual(a Options, b Options) bool {
	return a.DialTimeout == b.DialTimeout &&
		a.TLSHandshakeTimeout == b.TLSHandshakeTimeout &&
		a.ResponseHeaderTimeout == b.ResponseHeaderTimeout &&
		a.ExpectContinueTimeout == b.ExpectContinueTimeout &&
		a.IdleConnTimeout == b.IdleConnTimeout &&
		a.MaxIdleConns == b.MaxIdleConns &&
		a.MaxIdleConnsPerHost == b.MaxIdleConnsPerHost &&
		a.MaxConnsPerHost == b.MaxConnsPerHost
}
