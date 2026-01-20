package integration

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestAdminValidateDoesNotApply(t *testing.T) {
	upstreamA := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "A")
	})
	addrA, closeA := testutil.StartUpstream(t, upstreamA)
	defer closeA()

	upstreamB := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "B")
	})
	addrB, closeB := testutil.StartUpstream(t, upstreamB)
	defer closeB()

	initialConfig := fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1"}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, addrA)
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

	validateConfig := fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1"}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, addrB)
	resp, err := client.Do(mustAdminRequest(t, http.MethodPost, adminServer.URL+"/admin/validate", []byte(validateConfig)))
	if err != nil {
		t.Fatalf("validate request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	clientHTTP := &http.Client{Timeout: 2 * time.Second}
	resp, body := sendProxyRequest(t, clientHTTP, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !bytes.Equal(bytes.TrimSpace(body), []byte("A")) {
		t.Fatalf("expected A response, got %q", string(body))
	}
}
