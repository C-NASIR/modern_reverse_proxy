package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
)

func TestRetryNonIdempotentPost(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	goodAddr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	badAddr := unusedAddr(t)

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
					Retry: config.RetryConfig{
						Enabled:       true,
						MaxAttempts:   3,
						RetryOnErrors: []string{"dial"},
					},
				},
			},
		},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{badAddr, goodAddr}},
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
	lines := captureLogs(t, func() {
		resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodPost, "/")
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("expected 502, got %d", resp.StatusCode)
		}
		assertProxyError(t, resp, body, "upstream_connect_failed")
	})

	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &payload); err != nil {
		t.Fatalf("parse log json: %v", err)
	}
	if toInt(payload["retry_count"]) != 0 {
		t.Fatalf("expected retry_count 0")
	}
}
