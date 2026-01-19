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

func TestCohortRoutingSticky(t *testing.T) {
	traffic.SetSeedForTests(2)
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
						StableWeight: 50,
						CanaryWeight: 50,
						Cohort: config.CohortConfig{
							Enabled: true,
							Key:     "header:X-User-ID",
						},
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
	userA := map[string]string{"X-User-ID": "user-a"}
	counts := countVariantHeaders(t, client, proxyServer.URL, "example.local", "/", 20, userA)
	if counts["stable"] != 20 && counts["canary"] != 20 {
		t.Fatalf("expected sticky cohort for user-a, got counts %v", counts)
	}

	userB := map[string]string{"X-User-ID": "user-b"}
	counts = countVariantHeaders(t, client, proxyServer.URL, "example.local", "/", 10, userB)
	if counts["stable"] != 10 && counts["canary"] != 10 {
		t.Fatalf("expected sticky cohort for user-b, got counts %v", counts)
	}
}
