package integration

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"modern_reverse_proxy/internal/admin"
	"modern_reverse_proxy/internal/apply"
	"modern_reverse_proxy/internal/provider"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestAdminNotExposedOnDataPlane(t *testing.T) {
	store := runtime.NewStore(&runtime.Snapshot{Version: "v0", CreatedAt: time.Now().UTC(), Source: "file"})
	reg := registry.NewRegistry(0, 0)
	trafficReg := traffic.NewRegistry(0, 0)
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
	adminHandler := admin.NewHandler(admin.HandlerConfig{
		Store:        store,
		ApplyManager: applyManager,
		Auth:         auth,
		RateLimiter:  admin.NewRateLimiter(admin.RateLimitConfig{}),
		AdminStore:   admin.NewStore(),
	})
	adminServer := startAdminServer(t, adminHandler, adminTLS)
	defer adminServer.Close()

	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, nil, nil, nil)}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, _ := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/admin/snapshot")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on data plane, got %d", resp.StatusCode)
	}

	adminClient := testutil.NewAdminClient(t, testutil.AdminClientConfig{CAFile: ca.CertFile, ClientCert: &clientCert, Token: "secret", ServerName: "admin.local"})
	request, err := http.NewRequest(http.MethodGet, adminServer.URL+"/admin/snapshot", nil)
	if err != nil {
		t.Fatalf("build admin request: %v", err)
	}
	resp, err = adminClient.Do(request)
	if err != nil {
		t.Fatalf("admin request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on admin plane, got %d", resp.StatusCode)
	}
}
