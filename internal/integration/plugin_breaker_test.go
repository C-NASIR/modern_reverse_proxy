package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/plugin"
	"modern_reverse_proxy/internal/plugin/proto"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPluginBreakerBypassesCalls(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	upstreamAddr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	var pluginCalls atomic.Int32
	pluginAddr, closePlugin := testutil.StartPluginServer(t, testutil.PluginHandlers{
		ApplyRequest: func(context.Context, *pluginpb.ApplyRequestRequest) (*pluginpb.ApplyRequestResponse, error) {
			pluginCalls.Add(1)
			return nil, status.Error(codes.Unavailable, "boom")
		},
	})
	defer closePlugin()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	defer reg.Close()
	trafficReg := traffic.NewRegistry(0, 0)
	pluginReg := plugin.NewRegistry(0)
	defer pluginReg.Close()

	cfg := &config.Config{
		Routes: []config.Route{
			{
				ID:         "r1",
				Host:       "example.local",
				PathPrefix: "/",
				Pool:       "p1",
				Policy: config.RoutePolicy{
					Plugins: config.PluginConfig{
						Enabled: true,
						Filters: []config.PluginFilter{
							{
								Name:        "breaker",
								Addr:        pluginAddr,
								FailureMode: "fail_open",
								Breaker: config.PluginBreakerConfig{
									ConsecutiveFailures: 3,
									OpenMS:              500,
								},
							},
						},
					},
				},
			},
		},
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
	for i := 0; i < 3; i++ {
		resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
	}
	initialCalls := pluginCalls.Load()
	if initialCalls < 3 {
		t.Fatalf("expected plugin calls to reach breaker threshold")
	}

	for i := 0; i < 3; i++ {
		resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 after breaker open, got %d", resp.StatusCode)
		}
	}
	if pluginCalls.Load() != initialCalls {
		t.Fatalf("expected plugin calls to stop while breaker open")
	}
}
