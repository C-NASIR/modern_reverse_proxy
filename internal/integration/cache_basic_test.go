package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"modern_reverse_proxy/internal/cache"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestCacheBasicHitMiss(t *testing.T) {
	var upstreamCount int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&upstreamCount, 1)
		body := []byte("ok")
		w.Header().Set("Content-Length", "2")
		_, _ = w.Write(body)
	})

	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	defer reg.Close()
	trafficReg := traffic.NewRegistry(0, 0)

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
					Cache: config.CacheConfig{
						Enabled:             true,
						TTLMS:               2000,
						MaxObjectBytes:      1024 * 1024,
						CoalesceEnabled:     boolPtr(true),
						OnlyIfContentLength: boolPtr(true),
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

	cacheStore := cache.NewMemoryStore(cache.DefaultMaxObjectBytes)
	cacheCoalescer := cache.NewCoalescer(cache.DefaultMaxFlights)
	cacheLayer := cache.NewCache(cacheStore, cacheCoalescer)

	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{
		Store:    store,
		Registry: reg,
		Engine:   proxy.NewEngine(reg, nil, metrics, nil, nil),
		Metrics:  metrics,
		Cache:    cacheLayer,
	}
	metricsHandler := metrics.Handler()

	mux := http.NewServeMux()
	mux.Handle("/metrics", metricsHandler)
	mux.Handle("/", proxyHandler)

	proxyServer := httptest.NewServer(mux)
	defer proxyServer.Close()

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = oldStdout
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if string(body) != "ok" {
		t.Fatalf("unexpected body %q", string(body))
	}

	resp, body = sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if string(body) != "ok" {
		t.Fatalf("unexpected body %q", string(body))
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	if atomic.LoadInt32(&upstreamCount) != 1 {
		t.Fatalf("expected upstream count 1, got %d", upstreamCount)
	}

	lines := readLines(t, reader)
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d", len(lines))
	}

	var first map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("parse first log: %v", err)
	}
	if first["cache_status"] != "miss" {
		t.Fatalf("expected first cache_status miss, got %v", first["cache_status"])
	}

	var second map[string]interface{}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("parse second log: %v", err)
	}
	if second["cache_status"] != "hit" {
		t.Fatalf("expected second cache_status hit, got %v", second["cache_status"])
	}

	metricsText := fetchMetrics(t, proxyServer)
	if value, ok := metricValue(metricsText, "proxy_cache_requests_total", map[string]string{"route": "r1", "status": "hit"}); !ok || value < 1 {
		t.Fatalf("expected cache hit metric")
	}
	if value, ok := metricValue(metricsText, "proxy_cache_requests_total", map[string]string{"route": "r1", "status": "miss"}); !ok || value < 1 {
		t.Fatalf("expected cache miss metric")
	}
}

func boolPtr(value bool) *bool {
	return &value
}
