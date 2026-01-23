package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"modern_reverse_proxy/internal/breaker"
	"modern_reverse_proxy/internal/cache"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/outlier"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestBreakerIsolationAcrossRoutes(t *testing.T) {
	var routeAFailures atomic.Int32
	var routeBSuccess atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/a" {
			routeAFailures.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/b" {
			routeBSuccess.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	breakerReg := breaker.NewRegistry(0, 0)
	outlierReg := outlier.NewRegistry(0, 0, nil)
	trafficReg := traffic.NewRegistry(0, 0)
	defer reg.Close()
	defer breakerReg.Close()
	defer outlierReg.Close()
	defer trafficReg.Close()

	cfg := &config.Config{
		Routes: []config.Route{
			{ID: "rA", Host: "example.local", PathPrefix: "/a", Pool: "p1"},
			{ID: "rB", Host: "example.local", PathPrefix: "/b", Pool: "p2"},
		},
		Pools: map[string]config.Pool{
			"p1": {
				Endpoints: []string{addr},
				Breaker: config.BreakerConfig{
					Enabled:                     true,
					FailureRateThresholdPercent: 50,
					MinimumRequests:             2,
					EvaluationWindowMS:          500,
					OpenMS:                      1000,
				},
			},
			"p2": {
				Endpoints: []string{addr},
				Breaker: config.BreakerConfig{
					Enabled:                     true,
					FailureRateThresholdPercent: 50,
					MinimumRequests:             2,
					EvaluationWindowMS:          500,
					OpenMS:                      1000,
				},
			},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg, breakerReg, outlierReg, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{
		Store:           store,
		Registry:        reg,
		BreakerRegistry: breakerReg,
		OutlierRegistry: outlierReg,
		Engine:          proxy.NewEngine(reg, nil, nil, breakerReg, outlierReg),
	}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 2; i++ {
		resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/a")
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", resp.StatusCode)
		}
	}
	resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/a")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	assertProxyError(t, resp, body, "circuit_open")

	resp, _ = sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/b")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if routeBSuccess.Load() == 0 {
		t.Fatalf("expected route B to reach upstream")
	}
}

func TestRetryBudgetIsolation(t *testing.T) {
	var attemptsA atomic.Int32
	upstreamA := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := attemptsA.Add(1)
		if count == 1 {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "busy")
	})
	addrA, closeA := testutil.StartUpstream(t, upstreamA)
	defer closeA()

	var attemptsB atomic.Int32
	upstreamB := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := attemptsB.Add(1)
		switch count {
		case 1:
			w.WriteHeader(http.StatusOK)
		case 2:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 3:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	addrB, closeB := testutil.StartUpstream(t, upstreamB)
	defer closeB()

	reg := registry.NewRegistry(0, 0)
	trafficReg := traffic.NewRegistry(0, 0)
	retryReg := registry.NewRetryRegistry(0, 0)
	defer reg.Close()
	defer trafficReg.Close()
	defer retryReg.Close()

	policy := config.RoutePolicy{
		Retry: config.RetryConfig{
			Enabled:       true,
			MaxAttempts:   2,
			RetryOnStatus: []int{http.StatusServiceUnavailable},
		},
		RetryBudget: config.RetryBudgetConfig{
			Enabled:            true,
			PercentOfSuccesses: 100,
			Burst:              1,
		},
	}

	cfg := &config.Config{
		Routes: []config.Route{
			{ID: "rA", Host: "a.local", PathPrefix: "/", Pool: "p1", Policy: policy},
			{ID: "rB", Host: "b.local", PathPrefix: "/", Pool: "p2", Policy: policy},
		},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{addrA}},
			"p2": {Endpoints: []string{addrB}},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{
		Store:         store,
		Registry:      reg,
		RetryRegistry: retryReg,
		Engine:        proxy.NewEngine(reg, retryReg, nil, nil, nil),
	}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, _ := sendProxyRequest(t, client, proxyServer.URL, "a.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	resp, _ = sendProxyRequest(t, client, proxyServer.URL, "b.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	resp, _ = sendProxyRequest(t, client, proxyServer.URL, "a.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	attemptsAfterA := attemptsA.Load()
	if attemptsAfterA < 3 {
		t.Fatalf("expected retry attempts for A, got %d", attemptsAfterA)
	}

	resp, _ = sendProxyRequest(t, client, proxyServer.URL, "a.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	if attemptsA.Load() != attemptsAfterA+1 {
		t.Fatalf("expected no retry for exhausted budget")
	}

	resp, _ = sendProxyRequest(t, client, proxyServer.URL, "b.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if attemptsB.Load() != 3 {
		t.Fatalf("expected retries to remain for B")
	}
}

func TestCacheIsolationAcrossRoutes(t *testing.T) {
	var upstreamCount atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCount.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, r.Host)
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	trafficReg := traffic.NewRegistry(0, 0)
	defer reg.Close()
	defer trafficReg.Close()

	cachePolicy := config.CacheConfig{Enabled: true, TTLMS: 2000, MaxObjectBytes: 1024 * 1024}
	cfg := &config.Config{
		Routes: []config.Route{
			{ID: "rA", Host: "a.local", PathPrefix: "/", Pool: "p1", Policy: config.RoutePolicy{Cache: cachePolicy}},
			{ID: "rB", Host: "b.local", PathPrefix: "/", Pool: "p1", Policy: config.RoutePolicy{Cache: cachePolicy}},
		},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{addr}},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	cacheLayer := cache.NewCache(cache.NewMemoryStore(cache.DefaultMaxObjectBytes), cache.NewCoalescer(cache.DefaultMaxFlights))
	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, nil, nil, nil), Cache: cacheLayer}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, _ := sendProxyRequest(t, client, proxyServer.URL, "a.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	resp, _ = sendProxyRequest(t, client, proxyServer.URL, "b.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	resp, _ = sendProxyRequest(t, client, proxyServer.URL, "a.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if upstreamCount.Load() != 2 {
		t.Fatalf("expected cache isolation across routes")
	}
}
