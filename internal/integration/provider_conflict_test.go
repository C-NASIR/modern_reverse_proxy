package integration

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestProviderConflictResolution(t *testing.T) {
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

	fileConfig := fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{
  "id": "r1",
  "host": "example.local",
  "path_prefix": "/",
  "pool": "p1",
  "policy": {
    "request_timeout_ms": 1000,
    "traffic": {
      "enabled": true,
      "stable_pool": "p1",
      "canary_pool": "p2",
      "stable_weight": 100,
      "canary_weight": 0
    }
  }
}],
"pools": {
  "p1": {"endpoints": ["%s"]},
  "p2": {"endpoints": ["%s"]}
}
}`, addrA, addrB)
	filePath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(filePath, []byte(fileConfig), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	initialCfg, err := config.ParseJSON([]byte(fileConfig))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}

	reg := registry.NewRegistry(0, 0)
	trafficReg := traffic.NewRegistry(0, 0)
	snap, err := runtime.BuildSnapshot(initialCfg, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, nil, nil, nil)}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	fileProvider := provider.NewFileProvider(filePath)
	adminProvider := provider.NewAdminPush()
	applyManager := apply.NewManager(apply.ManagerConfig{
		Store:           store,
		Registry:        reg,
		TrafficRegistry: trafficReg,
		Providers:       []provider.Provider{fileProvider, adminProvider},
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

	conflictConfig := fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{
  "id": "r1",
  "host": "example.local",
  "path_prefix": "/",
  "pool": "p1",
  "policy": {"request_timeout_ms": 2000}
}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, addrA)
	resp, err := client.Do(mustAdminRequest(t, http.MethodPost, adminServer.URL+"/admin/config", []byte(conflictConfig)))
	if err != nil {
		t.Fatalf("conflict request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	if !bytes.Contains(bytes.ToLower(body), []byte("conflict")) {
		t.Fatalf("expected conflict error, got %q", string(body))
	}

	clientHTTP := &http.Client{Timeout: 2 * time.Second}
	proxyResp, proxyBody := sendProxyRequest(t, clientHTTP, proxyServer.URL, "example.local", http.MethodGet, "/")
	if proxyResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", proxyResp.StatusCode)
	}
	if !bytes.Equal(bytes.TrimSpace(proxyBody), []byte("A")) {
		t.Fatalf("expected A response, got %q", string(proxyBody))
	}

	badOverlayConfig := fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{
  "id": "r1",
  "host": "example.local",
  "path_prefix": "/",
  "pool": "p1",
  "overlay": true,
  "policy": {"request_timeout_ms": 2000}
}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, addrA)
	resp, err = client.Do(mustAdminRequest(t, http.MethodPost, adminServer.URL+"/admin/config", []byte(badOverlayConfig)))
	if err != nil {
		t.Fatalf("overlay request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	goodOverlayConfig := fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{
  "id": "r1",
  "host": "example.local",
  "path_prefix": "/",
  "pool": "p1",
  "overlay": true,
  "policy": {
    "request_timeout_ms": 1000,
    "traffic": {
      "enabled": true,
      "stable_pool": "p1",
      "canary_pool": "p2",
      "stable_weight": 80,
      "canary_weight": 20
    }
  }
}],
"pools": {
  "p1": {"endpoints": ["%s"]},
  "p2": {"endpoints": ["%s"]}
}
}`, addrA, addrB)
	resp, err = client.Do(mustAdminRequest(t, http.MethodPost, adminServer.URL+"/admin/config", []byte(goodOverlayConfig)))
	if err != nil {
		t.Fatalf("overlay request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
