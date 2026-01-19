package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
)

func TestMetricsCardinalityTopK(t *testing.T) {
	upstream := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Upstream", name)
			_, _ = io.WriteString(w, name)
		}
	}

	addr1, close1 := testutil.StartUpstream(t, upstream("A"))
	defer close1()
	addr2, close2 := testutil.StartUpstream(t, upstream("B"))
	defer close2()
	addr3, close3 := testutil.StartUpstream(t, upstream("C"))
	defer close3()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	defer reg.Close()

	metrics := obs.NewMetrics(obs.MetricsConfig{RouteTopK: 2, PoolTopK: 1, RecomputeInterval: 50 * time.Millisecond})
	obs.SetDefaultMetrics(metrics)
	defer obs.SetDefaultMetrics(nil)

	cfg := &config.Config{
		Routes: []config.Route{
			{ID: "route1", Host: "example.local", PathPrefix: "/r1", Pool: "p1"},
			{ID: "route2", Host: "example.local", PathPrefix: "/r2", Pool: "p1"},
			{ID: "route3", Host: "example.local", PathPrefix: "/r3", Pool: "p2"},
			{ID: "route4", Host: "example.local", PathPrefix: "/r4", Pool: "p2"},
			{ID: "route5", Host: "example.local", PathPrefix: "/r5", Pool: "p3"},
		},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{addr1}},
			"p2": {Endpoints: []string{addr2}},
			"p3": {Endpoints: []string{addr3}},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, metrics), Metrics: metrics}
	metricsHandler := metrics.Handler()

	mux := http.NewServeMux()
	mux.Handle("/metrics", metricsHandler)
	mux.Handle("/", proxyHandler)

	proxyServer := httptest.NewServer(mux)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 50; i++ {
		_, _ = sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/r1")
	}
	for i := 0; i < 40; i++ {
		_, _ = sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/r2")
	}
	for i := 0; i < 10; i++ {
		_, _ = sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/r3")
	}
	for i := 0; i < 5; i++ {
		_, _ = sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/r4")
	}
	_, _ = sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/r5")

	time.Sleep(100 * time.Millisecond)

	resp, err := proxyServer.Client().Get(proxyServer.URL + "/metrics")
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	text := string(body)

	routeLabels := extractLabelValues(text, "proxy_requests_total", "route")
	if !routeLabels["route1"] || !routeLabels["route2"] || !routeLabels["other"] {
		t.Fatalf("expected route1, route2, other labels; got %v", routeLabels)
	}
	if routeLabels["route3"] || routeLabels["route4"] || routeLabels["route5"] {
		t.Fatalf("unexpected low-traffic route labels: %v", routeLabels)
	}

	poolLabels := extractLabelValues(text, "proxy_upstream_roundtrip_seconds_bucket", "pool")
	if !poolLabels["p1"] || !poolLabels["other"] {
		t.Fatalf("expected p1 and other pool labels; got %v", poolLabels)
	}
	if poolLabels["p2"] || poolLabels["p3"] {
		t.Fatalf("unexpected pool labels: %v", poolLabels)
	}
}

func extractLabelValues(text string, metric string, label string) map[string]bool {
	values := map[string]bool{}
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, metric+"{") {
			continue
		}
		start := strings.Index(line, label+"=")
		if start == -1 {
			continue
		}
		start += len(label) + 2
		end := strings.Index(line[start:], "\"")
		if end == -1 {
			continue
		}
		value := line[start : start+end]
		values[value] = true
	}
	return values
}
