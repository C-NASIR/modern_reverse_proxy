package integration

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestStateReapAfterPoolRemoval(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	defer reg.Close()
	breakerReg := breaker.NewRegistry(20*time.Millisecond, 100*time.Millisecond)
	defer breakerReg.Close()
	outlierReg := outlier.NewRegistry(20*time.Millisecond, 100*time.Millisecond, nil)
	defer outlierReg.Close()
	trafficReg := traffic.NewRegistry(0, 0)

	cfg := &config.Config{
		Routes: []config.Route{{ID: "r1", Host: "example.local", PathPrefix: "/", Pool: "p1"}},
		Pools: map[string]config.Pool{
			"p1": {
				Endpoints: []string{addr},
				Breaker: config.BreakerConfig{
					Enabled:                     true,
					FailureRateThresholdPercent: 50,
					MinimumRequests:             1,
					EvaluationWindowMS:          200,
					OpenMS:                      200,
				},
				Outlier: config.OutlierConfig{
					Enabled:                     true,
					ConsecutiveFailures:         1,
					BaseEjectMS:                 100,
					MaxEjectMS:                  100,
					LatencyEnabled:              true,
					LatencyWindowSize:           8,
					LatencyEvalIntervalMS:       20,
					LatencyMinSamples:           1,
					LatencyMultiplier:           2,
					LatencyConsecutiveIntervals: 1,
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
	for i := 0; i < 3; i++ {
		_, _ = sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	}

	emptyCfg := &config.Config{}
	emptySnap, err := runtime.BuildSnapshot(emptyCfg, reg, breakerReg, outlierReg, trafficReg)
	if err != nil {
		t.Fatalf("build empty snapshot: %v", err)
	}
	if err := store.Swap(emptySnap); err != nil {
		t.Fatalf("swap snapshot: %v", err)
	}

	stableKey := "r1::p1"
	testutil.Eventually(t, 2*time.Second, 20*time.Millisecond, func() error {
		if breakerReg.Has(stableKey) {
			return fmt.Errorf("breaker still present")
		}
		if outlierReg.HasPool(stableKey) {
			return fmt.Errorf("outlier pool still present")
		}
		return nil
	})
}
