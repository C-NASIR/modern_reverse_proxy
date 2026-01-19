package integration

import (
	"errors"
	"net/http"
	"net/http/httptest"
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
	"modern_reverse_proxy/internal/traffic"
)

func TestCacheCoalescing(t *testing.T) {
	var upstreamCount int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&upstreamCount, 1)
		time.Sleep(100 * time.Millisecond)
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
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
			if resp.StatusCode != http.StatusOK {
				errs <- errors.New("unexpected status")
				return
			}
			if string(body) != "ok" {
				errs <- errors.New("unexpected body")
				return
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for requests")
	}
	close(errs)
	for err := range errs {
		t.Fatalf("unexpected response error: %v", err)
	}

	count := atomic.LoadInt32(&upstreamCount)
	if count > 2 {
		t.Fatalf("expected upstream count <= 2, got %d", count)
	}
}
