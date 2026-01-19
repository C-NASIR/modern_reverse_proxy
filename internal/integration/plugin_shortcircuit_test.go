package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

func TestPluginShortCircuit(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	upstreamAddr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	pluginAddr, closePlugin := testutil.StartPluginServer(t, testutil.PluginHandlers{
		ApplyRequest: func(context.Context, *pluginpb.ApplyRequestRequest) (*pluginpb.ApplyRequestResponse, error) {
			return &pluginpb.ApplyRequestResponse{
				Action:         pluginpb.ApplyRequestResponse_RESPOND,
				ResponseStatus: http.StatusUnauthorized,
				ResponseBody:   []byte("no"),
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
	metricsHandler := metrics.Handler()

	mux := http.NewServeMux()
	mux.Handle("/metrics", metricsHandler)
	mux.Handle("/", proxyHandler)

	proxyServer := httptest.NewServer(mux)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	if string(body) != "no" {
		t.Fatalf("expected body 'no', got %q", string(body))
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("expected upstream not called")
	}

	metricsText := fetchMetrics(t, proxyServer)
	value, ok := metricValue(metricsText, "proxy_plugin_shortcircuit_total", map[string]string{
		"filter": "authz",
	})
	if !ok || value < 1 {
		t.Fatalf("expected short-circuit metric increment")
	}
}
