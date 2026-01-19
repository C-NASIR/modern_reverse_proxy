package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestPluginTimeoutFailOpenBypasses(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
								Name:             "slow",
								Addr:             pluginAddr,
								RequestTimeoutMS: 10,
								FailureMode:      "fail_open",
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
	resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	logLines := readLines(t, reader)
	if len(logLines) == 0 {
		t.Fatalf("expected log line")
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(logLines[len(logLines)-1]), &payload); err != nil {
		t.Fatalf("parse log json: %v", err)
	}
	if bypassed, ok := payload["plugin_bypassed"].(bool); !ok || !bypassed {
		t.Fatalf("expected plugin_bypassed true")
	}

	metricsText := fetchMetrics(t, proxyServer)
	value, ok := metricValue(metricsText, "proxy_plugin_calls_total", map[string]string{
		"filter": "slow",
		"phase":  "request",
		"result": "timeout",
	})
	if !ok || value < 1 {
		t.Fatalf("expected timeout metric increment")
	}
}
