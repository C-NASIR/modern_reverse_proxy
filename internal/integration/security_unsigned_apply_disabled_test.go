package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"modern_reverse_proxy/internal/admin"
	"modern_reverse_proxy/internal/apply"
	"modern_reverse_proxy/internal/bundle"
	"modern_reverse_proxy/internal/provider"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestUnsignedApplyDisabledWhenPublicKeyConfigured(t *testing.T) {
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

	publicKey, privateKey := testutil.GenerateEd25519KeyPair(t)
	auth, err := admin.NewAuthenticator(admin.AuthConfig{Token: "secret", ClientCAFile: ca.CertFile})
	if err != nil {
		t.Fatalf("auth config: %v", err)
	}
	adminHandler := admin.NewHandler(admin.HandlerConfig{
		Store:         store,
		ApplyManager:  applyManager,
		Auth:          auth,
		RateLimiter:   admin.NewRateLimiter(admin.RateLimitConfig{}),
		AdminStore:    admin.NewStore(),
		PublicKey:     publicKey,
		AllowUnsigned: false,
	})
	adminServer := startAdminServer(t, adminHandler, adminTLS)
	defer adminServer.Close()

	adminClient := testutil.NewAdminClient(t, testutil.AdminClientConfig{CAFile: ca.CertFile, ClientCert: &clientCert, Token: "secret", ServerName: "admin.local"})

	configBytes := []byte(`{
"listen_addr":"127.0.0.1:0",
"routes":[{"id":"r1","host":"example.local","path_prefix":"/","pool":"p1"}],
"pools":{"p1":{"endpoints":["127.0.0.1:1"]}}
}`)
	resp, err := adminClient.Do(mustAdminRequest(t, http.MethodPost, adminServer.URL+"/admin/config", configBytes))
	if err != nil {
		t.Fatalf("unsigned apply: %v", err)
	}
	body, _ := readResponseBody(resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
	if !bytes.Contains(body, []byte("unsigned apply disabled")) {
		t.Fatalf("expected unsigned apply disabled error")
	}

	meta := bundle.Meta{
		Version:   apply.ConfigVersion(configBytes),
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Source:    "admin",
	}
	signed, err := bundle.NewSignedBundle(configBytes, meta, privateKey)
	if err != nil {
		t.Fatalf("sign bundle: %v", err)
	}
	payload, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	resp, err = adminClient.Do(mustAdminRequest(t, http.MethodPost, adminServer.URL+"/admin/bundle", payload))
	if err != nil {
		t.Fatalf("signed bundle: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func readResponseBody(resp *http.Response) ([]byte, error) {
	if resp == nil {
		return nil, nil
	}
	return io.ReadAll(resp.Body)
}
