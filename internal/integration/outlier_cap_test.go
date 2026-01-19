package integration

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/outlier"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestOutlierCapNeverEjectsAll(t *testing.T) {
	var firstCount atomic.Int32
	var secondCount atomic.Int32

	upstream := func(counter *atomic.Int32) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" {
				w.WriteHeader(http.StatusOK)
				return
			}
			counter.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}

	firstAddr, closeFirst := testutil.StartUpstream(t, upstream(&firstCount))
	defer closeFirst()
	secondAddr, closeSecond := testutil.StartUpstream(t, upstream(&secondCount))
	defer closeSecond()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	defer reg.Close()
	outlierReg := outlier.NewRegistry(0, 0, nil)
	defer outlierReg.Close()
	trafficReg := traffic.NewRegistry(0, 0)

	cfg := &config.Config{
		Routes: []config.Route{{ID: "r1", Host: "example.local", PathPrefix: "/", Pool: "p1"}},
		Pools: map[string]config.Pool{
			"p1": {
				Endpoints: []string{firstAddr, secondAddr},
				Health: config.HealthConfig{
					UnhealthyAfterFailures: 100,
				},
				Outlier: config.OutlierConfig{
					Enabled:             true,
					ConsecutiveFailures: 1,
					BaseEjectMS:         500,
					MaxEjectMS:          500,
					MaxEjectPercent:     50,
				},
			},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg, nil, outlierReg, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{
		Store:           store,
		Registry:        reg,
		OutlierRegistry: outlierReg,
		Engine:          proxy.NewEngine(reg, nil, nil, nil, outlierReg),
	}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 10; i++ {
		resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
		if resp.StatusCode == http.StatusBadGateway {
			t.Fatalf("expected upstream response, got %d", resp.StatusCode)
		}
	}

	if firstCount.Load()+secondCount.Load() == 0 {
		t.Fatalf("expected upstream attempts to continue")
	}
}
