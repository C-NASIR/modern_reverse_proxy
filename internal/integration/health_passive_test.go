package integration

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
)

func TestPassiveHealthEjectsEndpoint(t *testing.T) {
	badAddr := unusedAddress(t)
	upstreamGood := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "Good")
		_, _ = io.WriteString(w, "Good")
	})
	goodAddr, closeGood := testutil.StartUpstream(t, upstreamGood)
	defer closeGood()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	defer reg.Close()

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
			"p1": {
				Endpoints: []string{badAddr, goodAddr},
				Health: config.HealthConfig{
					UnhealthyAfterFailures: 1,
					BaseEjectMS:            500,
					MaxEjectMS:             1000,
				},
			},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg)}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(2 * time.Second)

	seenGood := false
	for time.Now().Before(deadline) {
		resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
		if resp.Header.Get("X-Upstream") == "Good" {
			seenGood = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !seenGood {
		t.Fatalf("did not see good upstream in time")
	}

	for i := 0; i < 10; i++ {
		resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
		if resp.Header.Get("X-Upstream") != "Good" {
			category := ""
			var payload proxy.ProxyErrorBody
			if err := json.Unmarshal(body, &payload); err == nil {
				category = payload.ErrorCategory
			}
			t.Fatalf("expected only good upstream after eject, got %q (status %d, category %q)", resp.Header.Get("X-Upstream"), resp.StatusCode, category)
		}
	}
}

func unusedAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()
	return addr
}
