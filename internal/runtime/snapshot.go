package runtime

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/policy"
	"modern_reverse_proxy/internal/router"
)

type Pool interface {
	Pick() string
}

type PoolRuntime struct {
	endpoints []string
	rr        uint64
}

func NewPoolRuntime(endpoints []string) *PoolRuntime {
	return &PoolRuntime{endpoints: append([]string(nil), endpoints...)}
}

func (p *PoolRuntime) Pick() string {
	if p == nil || len(p.endpoints) == 0 {
		return ""
	}
	idx := atomic.AddUint64(&p.rr, 1) - 1
	return p.endpoints[idx%uint64(len(p.endpoints))]
}

type Snapshot struct {
	Router *router.Router
	Pools  map[string]Pool
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
	defaultRequestTimeout               = 30 * time.Second
	defaultUpstreamDialTimeout          = time.Second
	defaultUpstreamResponseHeaderTimeout = 5 * time.Second
)

func BuildSnapshot(cfg *config.Config) (*Snapshot, error) {
	if cfg == nil {
		return nil, errors.New("config is nil")
	}

	pools := make(map[string]Pool, len(cfg.Pools))
	for name, pool := range cfg.Pools {
		if len(pool.Endpoints) == 0 {
			return nil, fmt.Errorf("pool %q has no endpoints", name)
		}
		pools[name] = NewPoolRuntime(pool.Endpoints)
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
			RequestTimeout:               durationOrDefault(route.Policy.RequestTimeoutMS, defaultRequestTimeout),
			UpstreamDialTimeout:          durationOrDefault(route.Policy.UpstreamDialTimeoutMS, defaultUpstreamDialTimeout),
			UpstreamResponseHeaderTimeout: durationOrDefault(route.Policy.UpstreamResponseHeaderTimeoutMS, defaultUpstreamResponseHeaderTimeout),
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

	return &Snapshot{
		Router: compiled,
		Pools:  pools,
	}, nil
}

func durationOrDefault(ms int, fallback time.Duration) time.Duration {
	if ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}
