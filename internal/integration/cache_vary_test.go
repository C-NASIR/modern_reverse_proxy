package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
)

func TestCacheVaryHeaders(t *testing.T) {
	var upstreamCount int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamCount, 1)
		value := r.Header.Get("Accept-Language")
		if value == "" {
			value = "none"
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(value)))
		_, _ = w.Write([]byte(value))
	})

	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	defer reg.Close()

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
						VaryHeaders:         []string{"Accept-Language"},
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

	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil)
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
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, body := sendVaryRequest(t, client, proxyServer.URL, "example.local", "en")
	if resp.StatusCode != http.StatusOK || string(body) != "en" {
		t.Fatalf("unexpected response for en: %d %q", resp.StatusCode, string(body))
	}

	resp, body = sendVaryRequest(t, client, proxyServer.URL, "example.local", "fr")
	if resp.StatusCode != http.StatusOK || string(body) != "fr" {
		t.Fatalf("unexpected response for fr: %d %q", resp.StatusCode, string(body))
	}

	resp, body = sendVaryRequest(t, client, proxyServer.URL, "example.local", "en")
	if resp.StatusCode != http.StatusOK || string(body) != "en" {
		t.Fatalf("unexpected response for en repeat: %d %q", resp.StatusCode, string(body))
	}

	if atomic.LoadInt32(&upstreamCount) != 2 {
		t.Fatalf("expected upstream count 2, got %d", upstreamCount)
	}
}

func sendVaryRequest(t *testing.T, client *http.Client, baseURL, host, language string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = host
	req.Header.Set("Accept-Language", language)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body.Close()
		t.Fatalf("read body: %v", err)
	}
	resp.Body.Close()
	return resp, body
}
