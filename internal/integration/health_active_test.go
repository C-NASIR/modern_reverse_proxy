package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
)

func TestActiveHealthRecoversEndpoint(t *testing.T) {
	var probeCount atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			count := probeCount.Add(1)
			if count <= 3 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		if probeCount.Load() < 4 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("X-Upstream", "A")
		_, _ = io.WriteString(w, "A")
	})

	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

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
				Endpoints: []string{addr},
				Health: config.HealthConfig{
					Path:                   "/healthz",
					IntervalMS:             50,
					TimeoutMS:              50,
					UnhealthyAfterFailures: 1,
					HealthyAfterSuccesses:  1,
					BaseEjectMS:            100,
					MaxEjectMS:             500,
				},
			},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, nil)}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}

	testutil.Eventually(t, 2*time.Second, 50*time.Millisecond, func() error {
		resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d (health probes: %d)", resp.StatusCode, probeCount.Load())
		}
		if resp.Header.Get("X-Upstream") != "A" {
			return fmt.Errorf("unexpected upstream %q", resp.Header.Get("X-Upstream"))
		}
		return nil
	})
}
