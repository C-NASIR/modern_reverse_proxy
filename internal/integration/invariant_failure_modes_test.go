package integration

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"modern_reverse_proxy/internal/breaker"
	"modern_reverse_proxy/internal/cache"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/plugin"
	"modern_reverse_proxy/internal/plugin/proto"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

type failingCacheStore struct{}

func (f failingCacheStore) Get(string) (cache.Entry, bool) {
	return cache.Entry{}, false
}

func (f failingCacheStore) Set(string, cache.Entry) error {
	return errors.New("cache store failure")
}

func (f failingCacheStore) Delete(string) {}

func TestFailureModeCircuitOpenSkipsUpstream(t *testing.T) {
	var upstreamCount atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		upstreamCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	defer reg.Close()
	breakerReg := breaker.NewRegistry(0, 0)
	defer breakerReg.Close()
	trafficReg := traffic.NewRegistry(0, 0)
	defer trafficReg.Close()

	cfg := &config.Config{
		Routes: []config.Route{{
			ID:         "r1",
			Host:       "example.local",
			PathPrefix: "/",
			Pool:       "p1",
		}},
		Pools: map[string]config.Pool{
			"p1": {
				Endpoints: []string{addr},
				Breaker: config.BreakerConfig{
					Enabled:                     true,
					FailureRateThresholdPercent: 50,
					MinimumRequests:             2,
					EvaluationWindowMS:          500,
					OpenMS:                      1000,
				},
			},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg, breakerReg, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{
		Store:           store,
		Registry:        reg,
		Engine:          proxy.NewEngine(reg, nil, nil, breakerReg, nil),
		BreakerRegistry: breakerReg,
	}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 2; i++ {
		resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", resp.StatusCode)
		}
	}

	resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	assertProxyError(t, resp, body, "circuit_open")
	if upstreamCount.Load() != 2 {
		t.Fatalf("expected upstream count 2, got %d", upstreamCount.Load())
	}
}

func TestFailureModeOverloadRejectSkipsUpstream(t *testing.T) {
	var upstreamCount atomic.Int32
	block := make(chan struct{})
	started := make(chan struct{})

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamCount.Add(1) == 1 {
			close(started)
			<-block
		}
		w.WriteHeader(http.StatusOK)
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	reg := registry.NewRegistry(0, 0)
	defer reg.Close()
	trafficReg := traffic.NewRegistry(0, 0)
	defer trafficReg.Close()

	cfg := &config.Config{
		Routes: []config.Route{{
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
		}},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{addr}},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, nil, nil, nil)}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	go func() {
		_, _ = sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	}()
	<-started

	resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	assertProxyError(t, resp, body, "overloaded")
	if upstreamCount.Load() != 1 {
		t.Fatalf("expected upstream count 1, got %d", upstreamCount.Load())
	}
	close(block)
}

func TestFailureModeCacheFailureDoesNotReturn5xx(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	reg := registry.NewRegistry(0, 0)
	defer reg.Close()
	trafficReg := traffic.NewRegistry(0, 0)
	defer trafficReg.Close()

	cfg := &config.Config{
		Routes: []config.Route{{
			ID:         "r1",
			Host:       "example.local",
			PathPrefix: "/",
			Pool:       "p1",
			Policy: config.RoutePolicy{
				Cache: config.CacheConfig{Enabled: true, TTLMS: 1000, MaxObjectBytes: 1024 * 1024},
			},
		}},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{addr}},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	cacheLayer := cache.NewCache(failingCacheStore{}, cache.NewCoalescer(cache.DefaultMaxFlights))
	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, nil, nil, nil), Cache: cacheLayer}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode >= http.StatusInternalServerError {
		t.Fatalf("expected non-5xx response, got %d", resp.StatusCode)
	}
}

func TestFailureModePluginFailOpenAllowsRequest(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	upstreamAddr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	pluginAddr, closePlugin := testutil.StartPluginServer(t, testutil.PluginHandlers{
		ApplyRequest: func(ctx context.Context, _ *pluginpb.ApplyRequestRequest) (*pluginpb.ApplyRequestResponse, error) {
			select {
			case <-time.After(50 * time.Millisecond):
				return &pluginpb.ApplyRequestResponse{Action: pluginpb.ApplyRequestResponse_CONTINUE}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	defer closePlugin()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	defer reg.Close()
	trafficReg := traffic.NewRegistry(0, 0)
	defer trafficReg.Close()
	pluginReg := plugin.NewRegistry(0)
	defer pluginReg.Close()

	cfg := &config.Config{
		Routes: []config.Route{{
			ID:         "r1",
			Host:       "example.local",
			PathPrefix: "/",
			Pool:       "p1",
			Policy: config.RoutePolicy{
				Plugins: config.PluginConfig{
					Enabled: true,
					Filters: []config.PluginFilter{{
						Name:             "slow",
						Addr:             pluginAddr,
						RequestTimeoutMS: 10,
						FailureMode:      "fail_open",
					}},
				},
			},
		}},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{upstreamAddr}},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{
		Store:          store,
		Registry:       reg,
		Engine:         proxy.NewEngine(reg, nil, nil, nil, nil),
		PluginRegistry: pluginReg,
	}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if upstreamCalls.Load() == 0 {
		t.Fatalf("expected upstream to be called")
	}
}

func TestFailureModePluginFailClosedBlocksRequest(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	upstreamAddr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	pluginAddr, closePlugin := testutil.StartPluginServer(t, testutil.PluginHandlers{
		ApplyRequest: func(ctx context.Context, _ *pluginpb.ApplyRequestRequest) (*pluginpb.ApplyRequestResponse, error) {
			select {
			case <-time.After(50 * time.Millisecond):
				return &pluginpb.ApplyRequestResponse{Action: pluginpb.ApplyRequestResponse_CONTINUE}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	defer closePlugin()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	defer reg.Close()
	trafficReg := traffic.NewRegistry(0, 0)
	defer trafficReg.Close()
	pluginReg := plugin.NewRegistry(0)
	defer pluginReg.Close()

	cfg := &config.Config{
		Routes: []config.Route{{
			ID:         "r1",
			Host:       "example.local",
			PathPrefix: "/",
			Pool:       "p1",
			Policy: config.RoutePolicy{
				Plugins: config.PluginConfig{
					Enabled: true,
					Filters: []config.PluginFilter{{
						Name:             "slow",
						Addr:             pluginAddr,
						RequestTimeoutMS: 10,
						FailureMode:      "fail_closed",
					}},
				},
			},
		}},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{upstreamAddr}},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{
		Store:          store,
		Registry:       reg,
		Engine:         proxy.NewEngine(reg, nil, nil, nil, nil),
		PluginRegistry: pluginReg,
	}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	assertProxyError(t, resp, body, "plugin_timeout")
	if upstreamCalls.Load() != 0 {
		t.Fatalf("expected upstream not called")
	}
}

func TestFailureModeMTLSRejectsWithForbidden(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	serverCert := testutil.WriteSelfSignedCert(t, "secure.local")
	clientCA := testutil.WriteCA(t, "client-ca")

	cfg := &config.Config{
		TLS: config.TLSConfig{
			Enabled:      true,
			Addr:         "127.0.0.1:0",
			ClientCAFile: clientCA.CertFile,
			Certs: []config.TLSCert{
				{ServerName: "secure.local", CertFile: serverCert.CertFile, KeyFile: serverCert.KeyFile},
			},
		},
		Routes: []config.Route{
			{ID: "secure", Host: "secure.local", PathPrefix: "/", Pool: "p1", Policy: config.RoutePolicy{RequireMTLS: true}},
		},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{addr}},
		},
	}

	proxyServer, _, _ := startTLSProxy(t, cfg)

	rootPool := x509CertPool(t, serverCert.Cert)
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: rootPool, ServerName: "secure.local"}},
	}
	resp, body := sendProxyRequest(t, client, "https://"+proxyServer.TLSAddr, "secure.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
	assertProxyError(t, resp, body, "mtls_required")
}
