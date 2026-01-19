package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
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

func TestCacheStreamingBreakaway(t *testing.T) {
	t.Run("not_cacheable_streaming", func(t *testing.T) {
		var upstreamCount int32
		upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&upstreamCount, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			for i := 0; i < 4; i++ {
				_, _ = io.WriteString(w, "data: ping\n\n")
				if flusher != nil {
					flusher.Flush()
				}
				time.Sleep(500 * time.Millisecond)
			}
		})

		addr, closeUpstream := testutil.StartUpstream(t, upstream)
		defer closeUpstream()

		proxyServer, closeProxy := startCacheProxy(t, addr, cacheConfigOptions{coalesceTimeoutMS: 200})
		defer closeProxy()

		oldStdout := os.Stdout
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		os.Stdout = writer
		defer func() {
			os.Stdout = oldStdout
		}()

		client := &http.Client{Timeout: 3 * time.Second}
		var wg sync.WaitGroup
		wg.Add(2)
		for i := 0; i < 2; i++ {
			go func() {
				defer wg.Done()
				resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
				if resp.StatusCode != http.StatusOK {
					t.Errorf("unexpected status %d", resp.StatusCode)
				}
			}()
		}
		wg.Wait()

		if err := writer.Close(); err != nil {
			t.Fatalf("close writer: %v", err)
		}

		if atomic.LoadInt32(&upstreamCount) != 2 {
			t.Fatalf("expected upstream count 2, got %d", upstreamCount)
		}

		lines := readLines(t, reader)
		if len(lines) != 2 {
			t.Fatalf("expected 2 log lines, got %d", len(lines))
		}
		for _, line := range lines {
			var payload map[string]interface{}
			if err := json.Unmarshal([]byte(line), &payload); err != nil {
				t.Fatalf("parse log: %v", err)
			}
			if payload["cache_status"] != "not_cacheable" {
				t.Fatalf("expected cache_status not_cacheable, got %v", payload["cache_status"])
			}
		}
	})

	t.Run("coalesce_breakaway", func(t *testing.T) {
		var upstreamCount int32
		upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&upstreamCount, 1)
			time.Sleep(500 * time.Millisecond)
			body := []byte("ok")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)
		})

		addr, closeUpstream := testutil.StartUpstream(t, upstream)
		defer closeUpstream()

		proxyServer, closeProxy := startCacheProxy(t, addr, cacheConfigOptions{coalesceTimeoutMS: 200})
		defer closeProxy()

		client := &http.Client{Timeout: 3 * time.Second}
		var wg sync.WaitGroup
		wg.Add(2)
		for i := 0; i < 2; i++ {
			go func() {
				defer wg.Done()
				resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
				if resp.StatusCode != http.StatusOK || string(body) != "ok" {
					t.Errorf("unexpected response: %d %q", resp.StatusCode, string(body))
				}
			}()
		}
		wg.Wait()

		if atomic.LoadInt32(&upstreamCount) != 2 {
			t.Fatalf("expected upstream count 2, got %d", upstreamCount)
		}

		metricsText := fetchMetrics(t, proxyServer)
		if value, ok := metricValue(metricsText, "proxy_cache_coalesce_breakaway_total", map[string]string{"route": "r1"}); !ok || value < 1 {
			t.Fatalf("expected breakaway metric")
		}
	})
}

type cacheConfigOptions struct {
	coalesceTimeoutMS int
}

func startCacheProxy(t *testing.T, upstreamAddr string, options cacheConfigOptions) (*httptest.Server, func()) {
	t.Helper()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)

	metrics := obs.NewMetrics(obs.MetricsConfig{})
	obs.SetDefaultMetrics(metrics)

	coalesceTimeout := 5000
	if options.coalesceTimeoutMS > 0 {
		coalesceTimeout = options.coalesceTimeoutMS
	}

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
						CoalesceTimeoutMS:   coalesceTimeout,
						OnlyIfContentLength: boolPtr(true),
					},
				},
			},
		},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{upstreamAddr}},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil)
	if err != nil {
		reg.Close()
		obs.SetDefaultMetrics(nil)
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

	closeProxy := func() {
		proxyServer.Close()
		reg.Close()
		obs.SetDefaultMetrics(nil)
	}

	return proxyServer, closeProxy
}
