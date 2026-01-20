package integration

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"
	"time"

	"modern_reverse_proxy/internal/admin"
	"modern_reverse_proxy/internal/apply"
	"modern_reverse_proxy/internal/provider"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestAdminAuthRequiresMTLSAndToken(t *testing.T) {
	ca := testutil.WriteCA(t, "admin-ca")
	serverCert := testutil.WriteServerCert(t, "admin.local", ca)
	clientCert := testutil.WriteClientCert(t, "client", ca)

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

	auth, err := admin.NewAuthenticator(admin.AuthConfig{Token: "secret", ClientCAFile: ca.CertFile})
	if err != nil {
		t.Fatalf("auth config: %v", err)
	}
	limiter := admin.NewRateLimiter(admin.RateLimitConfig{})
	adminStore := admin.NewStore()
	handler := admin.NewHandler(admin.HandlerConfig{
		Store:        store,
		ApplyManager: applyManager,
		Auth:         auth,
		RateLimiter:  limiter,
		AdminStore:   adminStore,
	})

	server := startAdminServer(t, handler, newAdminTLSConfig(t, serverCert.CertFile, serverCert.KeyFile, ca.CertFile))
	defer server.Close()

	body := []byte(fmt.Sprintf(`{
"routes": [{"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1"}],
"pools": {"p1": {"endpoints": ["127.0.0.1:1"]}}
}`))

	clientNoCert := testutil.NewAdminClient(t, testutil.AdminClientConfig{CAFile: ca.CertFile, Token: "secret", ServerName: "admin.local"})
	resp, err := clientNoCert.Do(mustAdminRequest(t, http.MethodPost, server.URL+"/admin/validate", body))
	if err != nil {
		t.Fatalf("request without cert: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}

	clientNoToken := testutil.NewAdminClient(t, testutil.AdminClientConfig{CAFile: ca.CertFile, ClientCert: &clientCert, ServerName: "admin.local"})
	resp, err = clientNoToken.Do(mustAdminRequest(t, http.MethodPost, server.URL+"/admin/validate", body))
	if err != nil {
		t.Fatalf("request without token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	clientBadToken := testutil.NewAdminClient(t, testutil.AdminClientConfig{CAFile: ca.CertFile, ClientCert: &clientCert, Token: "bad", ServerName: "admin.local"})
	resp, err = clientBadToken.Do(mustAdminRequest(t, http.MethodPost, server.URL+"/admin/validate", body))
	if err != nil {
		t.Fatalf("request with bad token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	clientGood := testutil.NewAdminClient(t, testutil.AdminClientConfig{CAFile: ca.CertFile, ClientCert: &clientCert, Token: "secret", ServerName: "admin.local"})
	resp, err = clientGood.Do(mustAdminRequest(t, http.MethodPost, server.URL+"/admin/validate", body))
	if err != nil {
		t.Fatalf("request with valid token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func mustAdminRequest(t *testing.T, method string, url string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}
