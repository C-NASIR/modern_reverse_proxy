package integration

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestCanarySplitWeighted(t *testing.T) {
	traffic.SetSeedForTests(1)
	stable := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Variant", "stable")
		w.WriteHeader(http.StatusOK)
	})
	canary := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Variant", "canary")
		w.WriteHeader(http.StatusOK)
	})

	stableAddr, closeStable := testutil.StartUpstream(t, stable)
	defer closeStable()
	canaryAddr, closeCanary := testutil.StartUpstream(t, canary)
	defer closeCanary()

	reg := registry.NewRegistry(0, 0)
	trafficReg := traffic.NewRegistry(0, 0)

	cfg := &config.Config{
		Routes: []config.Route{
			{
				ID:         "r1",
				Host:       "example.local",
				PathPrefix: "/",
				Pool:       "pStable",
				Policy: config.RoutePolicy{
					Traffic: config.TrafficConfig{
						Enabled:      true,
						StablePool:   "pStable",
						CanaryPool:   "pCanary",
						StableWeight: 80,
						CanaryWeight: 20,
					},
				},
			},
		},
		Pools: map[string]config.Pool{
			"pStable": {Endpoints: []string{stableAddr}},
			"pCanary": {Endpoints: []string{canaryAddr}},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, nil, nil, nil)}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	counts := countVariantHeaders(t, client, proxyServer.URL, "example.local", "/", 500, nil)
	canaryCount := counts["canary"]
	if canaryCount < 75 || canaryCount > 125 {
		t.Fatalf("expected canary count around 20%%, got %d", canaryCount)
	}
}
