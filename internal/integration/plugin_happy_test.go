package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/plugin"
	"modern_reverse_proxy/internal/plugin/proto"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestPluginHappyPath(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-From-Plugin") == "yes" {
			w.Header().Set("X-Upstream-From-Plugin", "yes")
		}
		w.WriteHeader(http.StatusOK)
	})
	upstreamAddr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	pluginAddr, closePlugin := testutil.StartPluginServer(t, testutil.PluginHandlers{
		ApplyRequest: func(context.Context, *pluginpb.ApplyRequestRequest) (*pluginpb.ApplyRequestResponse, error) {
			return &pluginpb.ApplyRequestResponse{
				Action:         pluginpb.ApplyRequestResponse_CONTINUE,
				MutatedHeaders: map[string]string{"X-From-Plugin": "yes"},
			}, nil
		},
		ApplyResponse: func(context.Context, *pluginpb.ApplyResponseRequest) (*pluginpb.ApplyResponseResponse, error) {
			return &pluginpb.ApplyResponseResponse{
				MutatedHeaders: map[string]string{"X-Resp-From-Plugin": "yes"},
			}, nil
		},
	})
	defer closePlugin()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	defer reg.Close()
	trafficReg := traffic.NewRegistry(0, 0)
	metrics := obs.NewMetrics(obs.MetricsConfig{})
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
								Name:        "authz",
								Addr:        pluginAddr,
								FailureMode: "fail_open",
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
		Engine:         proxy.NewEngine(reg, nil, metrics, nil, nil),
		Metrics:        metrics,
		PluginRegistry: pluginReg,
	}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.Header.Get("X-Upstream-From-Plugin") != "yes" {
		t.Fatalf("expected upstream to see plugin header")
	}
	if resp.Header.Get("X-Resp-From-Plugin") != "yes" {
		t.Fatalf("expected response header from plugin")
	}
}
