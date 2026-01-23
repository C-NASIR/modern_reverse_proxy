package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/server"
)

func TestMetricsTokenProtection(t *testing.T) {
	metrics := obs.NewMetrics(obs.MetricsConfig{})
	metrics.ObserveRequest("r1", "p1", http.StatusOK, time.Millisecond)
	metricsHandler := server.RequireBearerToken(metrics.Handler(), true, "metrics-secret")

	mux := http.NewServeMux()
	mux.Handle("/metrics", metricsHandler)
	metricsServer := httptest.NewServer(mux)
	defer metricsServer.Close()

	resp, err := metricsServer.Client().Get(metricsServer.URL + "/metrics")
	if err != nil {
		t.Fatalf("metrics request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, metricsServer.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer metrics-secret")
	resp, err = metricsServer.Client().Do(req)
	if err != nil {
		t.Fatalf("metrics request: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "proxy_requests_total") {
		t.Fatalf("expected proxy_requests_total in metrics")
	}
}
