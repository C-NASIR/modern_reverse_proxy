package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/pool"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestEndpointRemovalDrainsAndDeletes(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	var startedOnce atomic.Bool

	upstreamA := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if startedOnce.CompareAndSwap(false, true) {
			close(started)
		}
		<-block
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

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	defer reg.Close()
	trafficReg := traffic.NewRegistry(0, 0)

	cfg1 := &config.Config{
		Routes: []config.Route{
			{
				ID:         "r1",
				Host:       "example.local",
				PathPrefix: "/",
				Pool:       "p1",
			},
		},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{aAddr, bAddr}},
		},
	}

	snap1, err := runtime.BuildSnapshot(cfg1, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(snap1)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, nil, nil, nil)}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	responseCh := make(chan string, 1)

	go func() {
		resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
		responseCh <- resp.Header.Get("X-Upstream")
	}()

	<-started

	cfg2 := &config.Config{
		Routes: cfg1.Routes,
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{bAddr}},
		},
	}

	snap2, err := runtime.BuildSnapshot(cfg2, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	store.Swap(snap2)

	resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.Header.Get("X-Upstream") != "B" {
		t.Fatalf("expected upstream B after swap, got %q", resp.Header.Get("X-Upstream"))
	}

	close(block)
	select {
	case upstream := <-responseCh:
		if upstream != "A" {
			t.Fatalf("expected blocked request to A, got %q", upstream)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for blocked response")
	}

	poolKey := pool.PoolKey("p1")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !reg.HasEndpoint(poolKey, aAddr) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	if reg.HasEndpoint(poolKey, aAddr) {
		t.Fatalf("expected endpoint A to be removed")
	}
}
