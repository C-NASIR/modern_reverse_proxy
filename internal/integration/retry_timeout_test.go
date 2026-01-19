package integration

import (
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
)

func TestRetryTotalBudgetRespected(t *testing.T) {
	var attempts int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "slow")
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	reg := registry.NewRegistry(0, 0)
	defer reg.Close()

	retryReg := registry.NewRetryRegistry(0, 0)
	defer retryReg.Close()

	metrics := obs.NewMetrics(obs.MetricsConfig{})

	cfg := &config.Config{
		Routes: []config.Route{
			{
				ID:         "r1",
				Host:       "example.local",
				PathPrefix: "/",
				Pool:       "p1",
				Policy: config.RoutePolicy{
					RequestTimeoutMS: 500,
					Retry: config.RetryConfig{
						Enabled:            true,
						MaxAttempts:        5,
						PerTryTimeoutMS:    100,
						TotalRetryBudgetMS: 250,
						RetryOnStatus:      []int{http.StatusServiceUnavailable},
						RetryOnErrors:      []string{"timeout"},
					},
				},
			},
		},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{addr}},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{
		Store:         store,
		Registry:      reg,
		RetryRegistry: retryReg,
		Engine:        proxy.NewEngine(reg, retryReg, metrics),
		Metrics:       metrics,
	}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	start := time.Now()
	resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	_ = body

	duration := time.Since(start)
	if duration > 600*time.Millisecond {
		t.Fatalf("expected duration <= 600ms, got %v", duration)
	}
	if attempts > 3 {
		t.Fatalf("expected attempts <= 3, got %d", attempts)
	}
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d", resp.StatusCode)
	}
}
