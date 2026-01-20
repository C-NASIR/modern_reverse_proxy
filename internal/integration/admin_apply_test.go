package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"modern_reverse_proxy/internal/admin"
	"modern_reverse_proxy/internal/apply"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/provider"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestAdminApplySwapsSnapshot(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})

	a1Handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-block
		_, _ = io.WriteString(w, "A")
	})

	a1Addr, closeA1 := testutil.StartUpstream(t, a1Handler)
	defer closeA1()
	b1Addr, closeB1 := testutil.StartUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "B")
	}))
	defer closeB1()

	initialConfig := fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1"}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, a1Addr)
	cfg, err := config.ParseJSON([]byte(initialConfig))
	if err != nil {
		t.Fatalf("parse config: %v", err)
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

	adminProvider := provider.NewAdminPush()
	applyManager := apply.NewManager(apply.ManagerConfig{
		Store:           store,
		Registry:        reg,
		TrafficRegistry: trafficReg,
		Providers:       []provider.Provider{adminProvider},
		AdminProvider:   adminProvider,
	})

	ca := testutil.WriteCA(t, "admin-ca")
	serverCert := testutil.WriteServerCert(t, "admin.local", ca)
	clientCert := testutil.WriteClientCert(t, "client", ca)
	adminTLS := newAdminTLSConfig(t, serverCert.CertFile, serverCert.KeyFile, ca.CertFile)

	auth, err := admin.NewAuthenticator(admin.AuthConfig{Token: "secret", ClientCAFile: ca.CertFile})
	if err != nil {
		t.Fatalf("auth config: %v", err)
	}
	limiter := admin.NewRateLimiter(admin.RateLimitConfig{})
	adminStore := admin.NewStore()
	adminHandler := admin.NewHandler(admin.HandlerConfig{
		Store:        store,
		ApplyManager: applyManager,
		Auth:         auth,
		RateLimiter:  limiter,
		AdminStore:   adminStore,
	})
	adminServer := startAdminServer(t, adminHandler, adminTLS)
	defer adminServer.Close()

	client := testutil.NewAdminClient(t, testutil.AdminClientConfig{CAFile: ca.CertFile, ClientCert: &clientCert, Token: "secret", ServerName: "admin.local"})
	clientHTTP := &http.Client{Timeout: 5 * time.Second}
	responseCh := make(chan string, 1)
	responseErr := make(chan error, 1)

	go func() {
		resp, body := sendProxyRequest(t, clientHTTP, proxyServer.URL, "example.local", http.MethodGet, "/")
		if resp.StatusCode != http.StatusOK {
			responseErr <- fmt.Errorf("status %d", resp.StatusCode)
			return
		}
		responseCh <- string(body)
	}()

	<-started

	updatedConfig := fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1"}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, b1Addr)
	resp, err := client.Do(mustAdminRequest(t, http.MethodPost, adminServer.URL+"/admin/config", []byte(updatedConfig)))
	if err != nil {
		t.Fatalf("apply request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	resp, body := sendProxyRequest(t, clientHTTP, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !strings.HasPrefix(string(body), "B") {
		t.Fatalf("expected B response, got %q", string(body))
	}

	close(block)
	select {
	case err := <-responseErr:
		t.Fatalf("blocked request failed: %v", err)
	case body := <-responseCh:
		if !strings.HasPrefix(body, "A") {
			t.Fatalf("expected A response, got %q", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for blocked response")
	}
}
