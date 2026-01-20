package bench

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"modern_reverse_proxy/internal/cache"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/traffic"
)

func startBenchmarkProxy(b *testing.B, cfg *config.Config, cacheLayer *cache.Cache) (*httptest.Server, *http.Client, func()) {
	b.Helper()
	reg := registry.NewRegistry(0, 0)
	trafficReg := traffic.NewRegistry(0, 0)
	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil, trafficReg)
	if err != nil {
		reg.Close()
		trafficReg.Close()
		b.Fatalf("build snapshot: %v", err)
	}
	store := runtime.NewStore(snap)
	engine := proxy.NewEngine(reg, nil, nil, nil, nil)
	inflight := runtime.NewInflightTracker()
	handler := &proxy.Handler{Store: store, Registry: reg, Engine: engine, Cache: cacheLayer, Inflight: inflight}

	server := httptest.NewServer(handler)
	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 256,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	cleanup := func() {
		client.CloseIdleConnections()
		server.Close()
		reg.Close()
		trafficReg.Close()
	}

	return server, client, cleanup
}

func buildBaseConfig(endpoint string) *config.Config {
	return &config.Config{
		ListenAddr: "",
		Routes: []config.Route{
			{
				ID:         "r1",
				Host:       "example.com",
				PathPrefix: "/",
				Pool:       "p1",
				Policy:     config.RoutePolicy{},
			},
		},
		Pools: map[string]config.Pool{
			"p1": {
				Endpoints: []string{endpoint},
			},
		},
	}
}

func addCachePolicy(cfg *config.Config) {
	if cfg == nil || len(cfg.Routes) == 0 {
		return
	}
	cfg.Routes[0].Policy.Cache = config.CacheConfig{
		Enabled: true,
		Public:  true,
		TTLMS:   int(time.Minute / time.Millisecond),
	}
}

func addRetryPolicy(cfg *config.Config) {
	if cfg == nil || len(cfg.Routes) == 0 {
		return
	}
	cfg.Routes[0].Policy.Retry = config.RetryConfig{
		Enabled:       true,
		MaxAttempts:   2,
		RetryOnErrors: []string{"dial"},
	}
}

func buildRequest(url string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(url, "http") {
		req.Host = "example.com"
	}
	return req, nil
}

func newCacheLayer() *cache.Cache {
	store := cache.NewMemoryStore(0)
	coalescer := cache.NewCoalescer(0)
	return cache.NewCache(store, coalescer)
}
