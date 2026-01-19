package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestRouterPolicyRouting(t *testing.T) {
	upstreamA := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "A")
		_, _ = io.WriteString(w, "A")
	})
	upstreamB := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "B")
		_, _ = io.WriteString(w, "B")
	})

	aAddr, closeA := testutil.StartUpstream(t, upstreamA)
	defer closeA()
	bAddr, closeB := testutil.StartUpstream(t, upstreamB)
	defer closeB()

	cfg := &config.Config{
		Routes: []config.Route{
			{
				ID:         "api",
				Host:       "example.local",
				PathPrefix: "/api",
				Methods:    []string{"GET"},
				Pool:       "pApi",
			},
			{
				ID:         "root",
				Host:       "example.local",
				PathPrefix: "/",
				Pool:       "pRoot",
			},
		},
		Pools: map[string]config.Pool{
			"pApi":  {Endpoints: []string{bAddr}},
			"pRoot": {Endpoints: []string{aAddr}},
		},
	}

	reg := registry.NewRegistry(0, 0)
	trafficReg := traffic.NewRegistry(0, 0)
	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, nil, nil, nil)}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}

	resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/api/test")
	if resp.Header.Get("X-Upstream") != "B" {
		t.Fatalf("expected upstream B, got %q", resp.Header.Get("X-Upstream"))
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	_ = body

	resp, body = sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodPost, "/api/test")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	assertProxyError(t, resp, body, "no_route")

	resp, _ = sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/other")
	if resp.Header.Get("X-Upstream") != "A" {
		t.Fatalf("expected upstream A, got %q", resp.Header.Get("X-Upstream"))
	}
}

func TestRequestTimeoutReturnsGatewayTimeout(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			_, _ = io.WriteString(w, "late")
		}
	})

	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	cfg := &config.Config{
		Routes: []config.Route{
			{
				ID:         "slow",
				Host:       "example.local",
				PathPrefix: "/",
				Pool:       "slowPool",
				Policy: config.RoutePolicy{
					RequestTimeoutMS: 200,
				},
			},
		},
		Pools: map[string]config.Pool{
			"slowPool": {Endpoints: []string{addr}},
		},
	}

	reg := registry.NewRegistry(0, 0)
	trafficReg := traffic.NewRegistry(0, 0)
	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, nil, nil, nil)}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d", resp.StatusCode)
	}
	assertProxyError(t, resp, body, "request_timeout")
}

func sendProxyRequest(t *testing.T, client *http.Client, baseURL, host, method, path string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, baseURL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = host
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body.Close()
		t.Fatalf("read body: %v", err)
	}
	resp.Body.Close()
	return resp, body
}

func assertProxyError(t *testing.T, resp *http.Response, body []byte, category string) {
	t.Helper()
	requestID := resp.Header.Get(proxy.RequestIDHeader)
	if requestID == "" {
		t.Fatalf("expected request id header")
	}

	var payload proxy.ProxyErrorBody
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if payload.ErrorCategory != category {
		t.Fatalf("expected category %q, got %q", category, payload.ErrorCategory)
	}
	if payload.RequestID == "" {
		t.Fatalf("expected request id in body")
	}
	if payload.RequestID != requestID {
		t.Fatalf("request id header/body mismatch")
	}
}
