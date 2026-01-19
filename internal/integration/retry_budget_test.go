package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestRetryBudgetExhaustion(t *testing.T) {
	var attempts int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "busy")
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	reg := registry.NewRegistry(0, 0)
	defer reg.Close()
	trafficReg := traffic.NewRegistry(0, 0)

	retryReg := registry.NewRetryRegistry(0, 0)
	defer retryReg.Close()

	metrics := obs.NewMetrics(obs.MetricsConfig{})
	obs.SetDefaultMetrics(metrics)
	defer obs.SetDefaultMetrics(nil)

	cfg := &config.Config{
		Routes: []config.Route{
			{
				ID:         "r1",
				Host:       "example.local",
				PathPrefix: "/",
				Pool:       "p1",
				Policy: config.RoutePolicy{
					Retry: config.RetryConfig{
						Enabled:         true,
						MaxAttempts:     3,
						RetryOnStatus:   []int{http.StatusServiceUnavailable},
						RetryOnErrors:   []string{"dial"},
						BackoffMS:       0,
						BackoffJitterMS: 0,
					},
					RetryBudget: config.RetryBudgetConfig{
						Enabled:            true,
						PercentOfSuccesses: 0,
						Burst:              0,
					},
				},
			},
		},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{addr}},
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
		Engine:        proxy.NewEngine(reg, retryReg, metrics, nil, nil),
		Metrics:       metrics,
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Handler())
	mux.Handle("/", proxyHandler)

	proxyServer := httptest.NewServer(mux)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	lines := captureLogs(t, func() {
		resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d", resp.StatusCode)
		}
	})

	if atomic.LoadInt32(&attempts) != 1 {
		t.Fatalf("expected 1 upstream attempt, got %d", attempts)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &payload); err != nil {
		t.Fatalf("parse log json: %v", err)
	}
	if exhausted, ok := payload["retry_budget_exhausted"].(bool); !ok || !exhausted {
		t.Fatalf("expected retry_budget_exhausted true")
	}

	resp, err := proxyServer.Client().Get(proxyServer.URL + "/metrics")
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	count, ok := parseMetricCount(string(body), "proxy_retry_budget_exhausted_total")
	if !ok || count < 1 {
		t.Fatalf("expected proxy_retry_budget_exhausted_total >= 1, got %f", count)
	}
}
