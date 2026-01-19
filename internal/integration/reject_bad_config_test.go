package integration

import (
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
)

func TestRejectBadConfigKeepsOldSnapshot(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "A")
		_, _ = io.WriteString(w, "A")
	})

	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	goodCfg := &config.Config{
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

	reg := registry.NewRegistry(0, 0)
	snap, err := runtime.BuildSnapshot(goodCfg, reg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg)}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.Header.Get("X-Upstream") != "A" {
		t.Fatalf("expected upstream A, got %q", resp.Header.Get("X-Upstream"))
	}

	badCfg := &config.Config{
		Routes: []config.Route{
			{
				ID:         "r2",
				Host:       "example.local",
				PathPrefix: "/",
				Pool:       "missing",
			},
		},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{addr}},
		},
	}

	if _, err := runtime.BuildSnapshot(badCfg, reg); err == nil {
		t.Fatalf("expected build snapshot to fail")
	}

	resp, _ = sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.Header.Get("X-Upstream") != "A" {
		t.Fatalf("expected upstream A after bad config, got %q", resp.Header.Get("X-Upstream"))
	}
}
