package config

import (
	"encoding/json"
	"errors"

	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/router"
	"modern_reverse_proxy/internal/runtime"
)

type Config struct {
	ListenAddr string          `json:"listen_addr"`
	Routes     []Route         `json:"routes"`
	Pools      map[string]Pool `json:"pools"`
}

type Route struct {
	ID         string `json:"id"`
	Host       string `json:"host"`
	PathPrefix string `json:"path_prefix"`
	Pool       string `json:"pool"`
}

type Pool struct {
	Endpoints []string `json:"endpoints"`
}

func ParseJSON(data []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func BuildSnapshot(cfg *Config) (*runtime.Snapshot, error) {
	if cfg == nil {
		return nil, errors.New("config is nil")
	}
	routes := make([]router.RouteConfig, 0, len(cfg.Routes))
	for _, r := range cfg.Routes {
		routes = append(routes, router.RouteConfig{
			ID:         r.ID,
			Host:       r.Host,
			PathPrefix: r.PathPrefix,
			Pool:       r.Pool,
		})
	}

	compiled := router.NewRouter(routes)
	if compiled == nil {
		return nil, errors.New("router build failed")
	}

	pools := make(map[string]runtime.Pool, len(cfg.Pools))
	for name, pool := range cfg.Pools {
		pools[name] = proxy.NewPoolRuntime(pool.Endpoints)
	}

	return &runtime.Snapshot{
		Router: compiled,
		Pools:  pools,
	}, nil
}
