package integration

import (
	"fmt"
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

func TestConfigRejectionKeepsSnapshot(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("A"))
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	initialConfig := fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1"}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, addr)
	cfg, err := config.ParseJSON([]byte(initialConfig))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}

	reg := registry.NewRegistry(0, 0)
	trafficReg := traffic.NewRegistry(0, 0)
	defer reg.Close()
	defer trafficReg.Close()
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
	adminHandler := admin.NewHandler(admin.HandlerConfig{
		Store:        store,
		ApplyManager: applyManager,
		Auth:         auth,
		RateLimiter:  admin.NewRateLimiter(admin.RateLimitConfig{}),
		AdminStore:   admin.NewStore(),
	})
	adminServer := startAdminServer(t, adminHandler, adminTLS)
	defer adminServer.Close()

	adminClient := testutil.NewAdminClient(t, testutil.AdminClientConfig{CAFile: ca.CertFile, ClientCert: &clientCert, Token: "secret", ServerName: "admin.local"})
	client := &http.Client{Timeout: 2 * time.Second}

	cert := testutil.WriteSelfSignedCert(t, "secure.local")

	badConfigs := map[string]string{
		"canary_without_pool": fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1",
"policy": {"traffic": {"enabled": true, "stable_pool": "p1", "stable_weight": 50, "canary_weight": 50}}}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, addr),
		"cache_without_ttl": fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1",
"policy": {"cache": {"enabled": true}}}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, addr),
		"retry_without_attempts": fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1",
"policy": {"retry": {"enabled": true}}}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, addr),
		"plugins_without_filters": fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1",
"policy": {"plugins": {"enabled": true}}}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, addr),
		"mtls_missing_ca": fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"tls": {"enabled": true, "addr": "127.0.0.1:0", "certs": [{"server_name": "secure.local", "cert_file": "%s", "key_file": "%s"}]},
"routes": [{"id": "r1", "host": "secure.local", "path_prefix": "/", "pool": "p1", "policy": {"require_mtls": true}}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, cert.CertFile, cert.KeyFile, addr),
		"metrics_token_missing": fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"metrics": {"require_token": true},
"routes": [{"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1"}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, addr),
	}

	t.Setenv("METRICS_TOKEN", "")

	for name, badConfig := range badConfigs {
		resp, err := adminClient.Do(mustAdminRequest(t, http.MethodPost, adminServer.URL+"/admin/config", []byte(badConfig)))
		if err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d", name, resp.StatusCode)
		}

		proxyResp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
		if proxyResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 after %s, got %d", name, proxyResp.StatusCode)
		}
		if string(body) != "A" {
			t.Fatalf("unexpected response after %s: %q", name, string(body))
		}
	}
}
