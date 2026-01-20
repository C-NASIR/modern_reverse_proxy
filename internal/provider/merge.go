package provider

import (
	"context"
	"reflect"
	"sort"

	"modern_reverse_proxy/internal/config"
)

type loadedConfig struct {
	provider Provider
	cfg      *config.Config
}

func Merge(ctx context.Context, providers []Provider) (*config.Config, error) {
	entries := make([]loadedConfig, 0, len(providers))
	for _, p := range providers {
		if p == nil {
			continue
		}
		cfg, err := p.Load(ctx)
		if err != nil {
			return nil, err
		}
		if cfg == nil {
			continue
		}
		entries = append(entries, loadedConfig{provider: p, cfg: cfg})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].provider.Priority() < entries[j].provider.Priority()
	})

	result := &config.Config{
		Pools: make(map[string]config.Pool),
	}
	routeIndex := make(map[string]int)
	routeProviders := make(map[string]string)
	poolProviders := make(map[string]string)
	listenProvider := ""
	tlsProvider := ""

	for _, entry := range entries {
		providerName := entry.provider.Name()
		cfg := entry.cfg

		if cfg.ListenAddr != "" {
			if result.ListenAddr == "" {
				result.ListenAddr = cfg.ListenAddr
				listenProvider = providerName
			} else if result.ListenAddr != cfg.ListenAddr {
				return nil, &ConflictError{
					ObjectType:       "config",
					ObjectID:         "listen_addr",
					Field:            "listen_addr",
					ExistingProvider: listenProvider,
					IncomingProvider: providerName,
				}
			}
		}

		if !tlsEmpty(cfg.TLS) {
			if tlsEmpty(result.TLS) {
				result.TLS = cfg.TLS
				tlsProvider = providerName
			} else if !reflect.DeepEqual(result.TLS, cfg.TLS) {
				return nil, &ConflictError{
					ObjectType:       "config",
					ObjectID:         "tls",
					Field:            "tls",
					ExistingProvider: tlsProvider,
					IncomingProvider: providerName,
				}
			}
		}

		for _, route := range cfg.Routes {
			if route.ID == "" {
				continue
			}
			idx, ok := routeIndex[route.ID]
			if !ok {
				clean := route
				clean.Overlay = false
				result.Routes = append(result.Routes, clean)
				routeIndex[route.ID] = len(result.Routes) - 1
				routeProviders[route.ID] = providerName
				continue
			}

			existing := result.Routes[idx]
			if routesEqual(existing, route) {
				continue
			}
			if route.Overlay {
				merged, field, err := overlayRoute(existing, route)
				if err != nil {
					return nil, &ConflictError{
						ObjectType:       "route",
						ObjectID:         route.ID,
						Field:            field,
						ExistingProvider: routeProviders[route.ID],
						IncomingProvider: providerName,
					}
				}
				result.Routes[idx] = merged
				routeProviders[route.ID] = providerName
				continue
			}

			return nil, &ConflictError{
				ObjectType:       "route",
				ObjectID:         route.ID,
				Field:            routeConflictField(existing, route),
				ExistingProvider: routeProviders[route.ID],
				IncomingProvider: providerName,
			}
		}

		for name, pool := range cfg.Pools {
			if name == "" {
				continue
			}
			existing, ok := result.Pools[name]
			if !ok {
				clean := pool
				clean.Overlay = false
				result.Pools[name] = clean
				poolProviders[name] = providerName
				continue
			}
			if poolsEqual(existing, pool) {
				continue
			}
			if pool.Overlay {
				merged, field, err := overlayPool(existing, pool)
				if err != nil {
					return nil, &ConflictError{
						ObjectType:       "pool",
						ObjectID:         name,
						Field:            field,
						ExistingProvider: poolProviders[name],
						IncomingProvider: providerName,
					}
				}
				result.Pools[name] = merged
				poolProviders[name] = providerName
				continue
			}

			return nil, &ConflictError{
				ObjectType:       "pool",
				ObjectID:         name,
				Field:            poolConflictField(existing, pool),
				ExistingProvider: poolProviders[name],
				IncomingProvider: providerName,
			}
		}
	}

	if result.Pools == nil {
		result.Pools = make(map[string]config.Pool)
	}
	return result, nil
}

func tlsEmpty(cfg config.TLSConfig) bool {
	return !cfg.Enabled && cfg.Addr == "" && len(cfg.Certs) == 0 && cfg.ClientCAFile == "" && cfg.MinVersion == "" && len(cfg.CipherSuites) == 0
}

func routesEqual(a config.Route, b config.Route) bool {
	a.Overlay = false
	b.Overlay = false
	return reflect.DeepEqual(a, b)
}

func poolsEqual(a config.Pool, b config.Pool) bool {
	a.Overlay = false
	b.Overlay = false
	return reflect.DeepEqual(a, b)
}

func overlayRoute(base config.Route, overlay config.Route) (config.Route, string, error) {
	normalized := overlay
	normalized.Overlay = false
	normalized.Policy.Traffic.StableWeight = base.Policy.Traffic.StableWeight
	normalized.Policy.Traffic.CanaryWeight = base.Policy.Traffic.CanaryWeight
	if !routesEqual(base, normalized) {
		return config.Route{}, routeConflictField(base, normalized), errConflict
	}
	merged := base
	merged.Policy.Traffic.StableWeight = overlay.Policy.Traffic.StableWeight
	merged.Policy.Traffic.CanaryWeight = overlay.Policy.Traffic.CanaryWeight
	merged.Overlay = false
	return merged, "", nil
}

func overlayPool(base config.Pool, overlay config.Pool) (config.Pool, string, error) {
	normalized := overlay
	normalized.Overlay = false
	normalized.Endpoints = base.Endpoints
	if !poolsEqual(base, normalized) {
		return config.Pool{}, poolConflictField(base, normalized), errConflict
	}
	merged := base
	merged.Endpoints = overlay.Endpoints
	merged.Overlay = false
	return merged, "", nil
}

var errConflict = &ConflictError{}

func routeConflictField(a config.Route, b config.Route) string {
	if a.Host != b.Host {
		return "host"
	}
	if a.PathPrefix != b.PathPrefix {
		return "path_prefix"
	}
	if !reflect.DeepEqual(a.Methods, b.Methods) {
		return "methods"
	}
	if a.Pool != b.Pool {
		return "pool"
	}
	if a.Policy.RequestTimeoutMS != b.Policy.RequestTimeoutMS {
		return "policy.request_timeout_ms"
	}
	if a.Policy.UpstreamDialTimeoutMS != b.Policy.UpstreamDialTimeoutMS {
		return "policy.upstream_dial_timeout_ms"
	}
	if a.Policy.UpstreamResponseHeaderTimeoutMS != b.Policy.UpstreamResponseHeaderTimeoutMS {
		return "policy.upstream_response_header_timeout_ms"
	}
	if a.Policy.RequireMTLS != b.Policy.RequireMTLS {
		return "policy.require_mtls"
	}
	if a.Policy.MTLSClientCA != b.Policy.MTLSClientCA {
		return "policy.mtls_client_ca"
	}
	if !reflect.DeepEqual(a.Policy.Retry, b.Policy.Retry) {
		return "policy.retry"
	}
	if !reflect.DeepEqual(a.Policy.RetryBudget, b.Policy.RetryBudget) {
		return "policy.retry_budget"
	}
	if !reflect.DeepEqual(a.Policy.ClientRetryCap, b.Policy.ClientRetryCap) {
		return "policy.client_retry_cap"
	}
	if !reflect.DeepEqual(a.Policy.Cache, b.Policy.Cache) {
		return "policy.cache"
	}
	if !reflect.DeepEqual(a.Policy.Plugins, b.Policy.Plugins) {
		return "policy.plugins"
	}
	if !reflect.DeepEqual(a.Policy.Traffic, b.Policy.Traffic) {
		return "policy.traffic"
	}
	return "route"
}

func poolConflictField(a config.Pool, b config.Pool) string {
	if !reflect.DeepEqual(a.Endpoints, b.Endpoints) {
		return "endpoints"
	}
	if !reflect.DeepEqual(a.Health, b.Health) {
		return "health"
	}
	if !reflect.DeepEqual(a.Breaker, b.Breaker) {
		return "breaker"
	}
	if !reflect.DeepEqual(a.Outlier, b.Outlier) {
		return "outlier"
	}
	return "pool"
}
