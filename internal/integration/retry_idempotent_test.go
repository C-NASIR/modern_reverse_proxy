package integration

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
)

func TestRetryIdempotentDialFailure(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Upstream", "Good")
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

	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil)
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
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		if resp.Header.Get("X-Upstream") != "Good" {
			t.Fatalf("expected Good upstream, got %q", resp.Header.Get("X-Upstream"))
		}
	})

	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &payload); err != nil {
		t.Fatalf("parse log json: %v", err)
	}
	retryCount := toInt(payload["retry_count"])
	if retryCount < 1 {
		t.Fatalf("expected retry_count >= 1, got %d", retryCount)
	}
	if payload["retry_last_reason"] == "" {
		t.Fatalf("expected retry_last_reason")
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
	metricCount, ok := parseMetricCount(string(body), "proxy_retries_total")
	if !ok || metricCount < 1 {
		t.Fatalf("expected proxy_retries_total >= 1, got %f", metricCount)
	}
}

func captureLogs(t *testing.T, fn func()) []string {
	t.Helper()
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = writer
	fn()
	if err := writer.Close(); err != nil {
		os.Stdout = oldStdout
		t.Fatalf("close writer: %v", err)
	}
	os.Stdout = oldStdout
	return readLines(t, reader)
}

func unusedAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

func toInt(value interface{}) int {
	if value == nil {
		return 0
	}
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
