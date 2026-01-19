package integration

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"modern_reverse_proxy/internal/breaker"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/outlier"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestBreakerPersistsAcrossSnapshotSwap(t *testing.T) {
	var upstreamCount atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		upstreamCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	defer reg.Close()
	breakerReg := breaker.NewRegistry(0, 0)
	defer breakerReg.Close()
	outlierReg := outlier.NewRegistry(0, 0, nil)
	defer outlierReg.Close()
	trafficReg := traffic.NewRegistry(0, 0)

	cfg := &config.Config{
		Routes: []config.Route{{
			ID:         "r1",
			Host:       "example.local",
			PathPrefix: "/",
			Pool:       "p1",
		}},
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
		resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", resp.StatusCode)
		}
	}

	nextCfg := &config.Config{
		Routes: []config.Route{{
			ID:         "r1",
			Host:       "example.local",
			PathPrefix: "/",
			Pool:       "p1",
			Policy: config.RoutePolicy{
				RequestTimeoutMS: 1500,
			},
		}},
		Pools: cfg.Pools,
	}

	nextSnap, err := runtime.BuildSnapshot(nextCfg, reg, breakerReg, outlierReg, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	store.Swap(nextSnap)

	resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	assertProxyError(t, resp, body, "circuit_open")

	if upstreamCount.Load() != 2 {
		t.Fatalf("expected 2 upstream requests, got %d", upstreamCount.Load())
	}
}
