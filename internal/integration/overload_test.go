package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
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

func TestOverloadLimiterRejects(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	reg := registry.NewRegistry(0, 0)
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
					Traffic: config.TrafficConfig{
						Enabled:      true,
						StablePool:   "p1",
						StableWeight: 100,
						Overload: config.OverloadConfig{
							Enabled:     true,
							MaxInflight: 1,
							MaxQueue:    0,
						},
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
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, metrics, nil, nil), Metrics: metrics}
	metricsHandler := metrics.Handler()

	mux := http.NewServeMux()
	mux.Handle("/metrics", metricsHandler)
	mux.Handle("/", proxyHandler)

	proxyServer := httptest.NewServer(mux)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	results := make([]struct {
		resp *http.Response
		body []byte
		err  error
	}, 0, 5)
	var mu sync.Mutex

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodGet, proxyServer.URL+"/", nil)
			if err == nil {
				req.Host = "example.local"
				var resp *http.Response
				resp, err = client.Do(req)
				if err == nil {
					body, readErr := io.ReadAll(resp.Body)
					resp.Body.Close()
					if readErr != nil {
						err = readErr
					}
					mu.Lock()
					results = append(results, struct {
						resp *http.Response
						body []byte
						err  error
					}{resp: resp, body: body, err: err})
					mu.Unlock()
					return
				}
			}
			mu.Lock()
			results = append(results, struct {
				resp *http.Response
				body []byte
				err  error
			}{resp: nil, body: nil, err: err})
			mu.Unlock()
		}()
	}
	wg.Wait()

	success := 0
	overloaded := 0
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("request error: %v", result.err)
		}
		switch result.resp.StatusCode {
		case http.StatusOK:
			success++
		case http.StatusServiceUnavailable:
			overloaded++
			assertProxyError(t, result.resp, result.body, "overloaded")
		default:
			t.Fatalf("unexpected status %d", result.resp.StatusCode)
		}
	}

	if success != 1 {
		t.Fatalf("expected 1 success, got %d", success)
	}
	if overloaded != 4 {
		t.Fatalf("expected 4 overloaded, got %d", overloaded)
	}

	metricsText := fetchMetrics(t, proxyServer)
	if value, ok := metricValue(metricsText, "proxy_overload_reject_total", map[string]string{"route": "r1"}); !ok || value < 1 {
		t.Fatalf("expected overload reject metric")
	}
}
