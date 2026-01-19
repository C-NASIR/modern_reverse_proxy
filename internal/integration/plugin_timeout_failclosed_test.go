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
)

func TestPluginTimeoutFailClosedRejects(t *testing.T) {
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
			case <-time.After(80 * time.Millisecond):
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
								Name:             "slow",
								Addr:             pluginAddr,
								RequestTimeoutMS: 10,
								FailureMode:      "fail_closed",
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
	resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	assertProxyError(t, resp, body, "plugin_timeout")
	if upstreamCalls.Load() != 0 {
		t.Fatalf("expected upstream not called")
	}
}
