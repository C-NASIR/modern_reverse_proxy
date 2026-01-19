package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestMetricsEndpointIncrements(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "A")
		_, _ = io.WriteString(w, "ok")
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	defer reg.Close()

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
			},
		},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{addr}},
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
	for i := 0; i < 3; i++ {
		_, _ = sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	}

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
	if !strings.Contains(text, "proxy_requests_total") {
		t.Fatalf("expected proxy_requests_total in metrics")
	}
	if !strings.Contains(text, "proxy_request_duration_seconds") {
		t.Fatalf("expected proxy_request_duration_seconds in metrics")
	}

	count, ok := parseMetricCount(text, "proxy_requests_total")
	if !ok || count < 3 {
		t.Fatalf("expected proxy_requests_total >= 3, got %f", count)
	}
}

func parseMetricCount(text string, metric string) (float64, bool) {
	total := 0.0
	found := false
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, metric) {
			parts := strings.Fields(line)
			if len(parts) < 2 {
				continue
			}
			value, err := parseFloat(parts[len(parts)-1])
			if err != nil {
				return 0, false
			}
			found = true
			total += value
		}
	}
	return total, found
}

func parseFloat(value string) (float64, error) {
	return strconv.ParseFloat(value, 64)
}
