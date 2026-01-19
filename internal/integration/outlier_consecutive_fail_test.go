package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/outlier"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
)

func TestOutlierConsecutiveFailuresEject(t *testing.T) {
	bad := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("X-Upstream", "bad")
		w.WriteHeader(http.StatusInternalServerError)
	})
	good := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("X-Upstream", "good")
		_, _ = io.WriteString(w, "ok")
	})

	badAddr, closeBad := testutil.StartUpstream(t, bad)
	defer closeBad()
	goodAddr, closeGood := testutil.StartUpstream(t, good)
	defer closeGood()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	defer reg.Close()
	metrics := obs.NewMetrics(obs.MetricsConfig{})
	obs.SetDefaultMetrics(metrics)
	defer obs.SetDefaultMetrics(nil)
	outlierReg := outlier.NewRegistry(0, 0, metrics.RecordOutlierEjection)
	defer outlierReg.Close()

	cfg := &config.Config{
		Routes: []config.Route{{ID: "r1", Host: "example.local", PathPrefix: "/", Pool: "p1"}},
		Pools: map[string]config.Pool{
			"p1": {
				Endpoints: []string{badAddr, goodAddr},
				Health: config.HealthConfig{
					UnhealthyAfterFailures: 100,
				},
				Outlier: config.OutlierConfig{
					Enabled:             true,
					ConsecutiveFailures: 2,
					BaseEjectMS:         500,
					MaxEjectMS:          500,
				},
			},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg, nil, outlierReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{
		Store:           store,
		Registry:        reg,
		OutlierRegistry: outlierReg,
		Engine:          proxy.NewEngine(reg, nil, metrics, nil, outlierReg),
		Metrics:         metrics,
	}

	metricsHandler := metrics.Handler()
	mux := http.NewServeMux()
	mux.Handle("/metrics", metricsHandler)
	mux.Handle("/", proxyHandler)

	proxyServer := httptest.NewServer(mux)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 6; i++ {
		_, _ = sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	}

	testutil.Eventually(t, 2*time.Second, 50*time.Millisecond, func() error {
		for i := 0; i < 5; i++ {
			resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
			if resp.Header.Get("X-Upstream") != "good" {
				return fmt.Errorf("expected good upstream, got %q", resp.Header.Get("X-Upstream"))
			}
		}
		return nil
	})

	text := fetchMetrics(t, proxyServer)
	count, ok := metricValue(text, "proxy_outlier_ejections_total", map[string]string{"reason": "consecutive_fail"})
	if !ok || count < 1 {
		t.Fatalf("expected outlier ejection metric to increase")
	}
}
