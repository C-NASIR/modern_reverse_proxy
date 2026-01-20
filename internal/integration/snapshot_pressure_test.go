package integration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"modern_reverse_proxy/internal/apply"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/provider"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestSnapshotPressureRejectsApply(t *testing.T) {
	block := make(chan struct{})
	startedCh := make(chan struct{}, 10)

	upstreamAddr, closeUpstream := testutil.StartUpstream(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		startedCh <- struct{}{}
		<-block
		writer.WriteHeader(http.StatusOK)
	}))
	defer closeUpstream()

	cfgJSON := proxyConfigWithRoute(upstreamAddr, "r1")
	cfg, err := config.ParseJSON([]byte(cfgJSON))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	reg := registry.NewRegistry(0, 0)
	trafficReg := traffic.NewRegistry(0, 0)
	initialSnap, err := runtime.BuildSnapshot(cfg, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	store := runtime.NewStore(initialSnap)
	store.SetMaxRetired(2)

	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, nil, nil, nil)}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	adminProvider := provider.NewAdminPush()
	applyManager := apply.NewManager(apply.ManagerConfig{
		Store:           store,
		Registry:        reg,
		TrafficRegistry: trafficReg,
		Providers:       []provider.Provider{adminProvider},
		AdminProvider:   adminProvider,
		Pressure:        runtime.NewPressure(store),
	})

	client := &http.Client{Timeout: 20 * time.Second}
	responseChans := []chan error{}
	responseChans = append(responseChans, startBlockedRequest(t, client, proxyServer.URL))
	awaitRequestStart(t, startedCh)

	applyConfigs := []string{
		proxyConfigWithRoute(upstreamAddr, "r1"),
		proxyConfigWithRoute(upstreamAddr, "r2"),
		proxyConfigWithRoute(upstreamAddr, "r3"),
		proxyConfigWithRoute(upstreamAddr, "r4"),
		proxyConfigWithRoute(upstreamAddr, "r5"),
	}

	pressureHit := false
	for _, raw := range applyConfigs {
		_, err := applyManager.Apply(context.Background(), []byte(raw), "admin", apply.ModeApply)
		if err != nil {
			if errors.Is(err, apply.ErrPressure) {
				pressureHit = true
				break
			}
			t.Fatalf("apply error: %v", err)
		}
		responseChans = append(responseChans, startBlockedRequest(t, client, proxyServer.URL))
		awaitRequestStart(t, startedCh)
	}
	if !pressureHit {
		t.Fatalf("expected pressure rejection")
	}

	close(block)
	for _, responseChan := range responseChans {
		select {
		case err := <-responseChan:
			if err != nil {
				t.Fatalf("blocked request error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for blocked request")
		}
	}
	store.Reap()

	_, err = applyManager.Apply(context.Background(), []byte(applyConfigs[0]), "admin", apply.ModeApply)
	if err != nil {
		t.Fatalf("expected apply after pressure relief: %v", err)
	}
}

func proxyConfigWithRoute(upstreamAddr string, routeID string) string {
	return fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{"id": "%s", "host": "example.local", "path_prefix": "/", "pool": "p1", "policy": {"request_timeout_ms": 20000, "upstream_response_header_timeout_ms": 20000}}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, routeID, upstreamAddr)
}

func startBlockedRequest(t *testing.T, client *http.Client, baseURL string) chan error {
	t.Helper()
	responseChan := make(chan error, 1)
	go func() {
		request, err := http.NewRequest(http.MethodGet, baseURL+"/", nil)
		if err != nil {
			responseChan <- err
			return
		}
		request.Host = "example.local"
		resp, err := client.Do(request)
		if err != nil {
			responseChan <- err
			return
		}
		resp.Body.Close()
		responseChan <- nil
	}()
	return responseChan
}

func awaitRequestStart(t *testing.T, startedCh <-chan struct{}) {
	t.Helper()
	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for upstream request")
	}
}
