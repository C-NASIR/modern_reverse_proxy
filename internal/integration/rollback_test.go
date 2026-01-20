package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"modern_reverse_proxy/internal/admin"
	"modern_reverse_proxy/internal/apply"
	"modern_reverse_proxy/internal/bundle"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/rollout"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestAdminRollbackRestoresPreviousBundle(t *testing.T) {
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

	configA := fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1"}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, addrA)
	configB := fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1"}],
"pools": {"p1": {"endpoints": ["%s"]}}
}`, addrB)

	keyPair := testutil.WriteEd25519KeyPair(t, "bundle")
	metaA := bundle.Meta{
		Version:   apply.ConfigVersion([]byte(configA)),
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Source:    "admin",
	}
	bundleA, err := bundle.NewSignedBundle([]byte(configA), metaA, keyPair.PrivateKey)
	if err != nil {
		t.Fatalf("bundle A sign: %v", err)
	}
	metaB := bundle.Meta{
		Version:   apply.ConfigVersion([]byte(configB)),
		CreatedAt: time.Now().Add(10 * time.Millisecond).UTC().Format(time.RFC3339Nano),
		Source:    "admin",
	}
	bundleB, err := bundle.NewSignedBundle([]byte(configB), metaB, keyPair.PrivateKey)
	if err != nil {
		t.Fatalf("bundle B sign: %v", err)
	}

	reg := registry.NewRegistry(0, 0)
	trafficReg := traffic.NewRegistry(0, 0)
	metrics := obs.NewMetrics(obs.MetricsConfig{})
	obs.SetDefaultMetrics(metrics)
	defer obs.SetDefaultMetrics(nil)

	initialCfg, err := config.ParseJSON([]byte(configA))
	if err != nil {
		t.Fatalf("parse config A: %v", err)
	}
	initialSnap, err := runtime.BuildSnapshot(initialCfg, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build initial snapshot: %v", err)
	}
	initialSnap.Version = metaA.Version
	initialSnap.Source = "bootstrap"
	store := runtime.NewStore(initialSnap)

	applyManager := apply.NewManager(apply.ManagerConfig{
		Store:           store,
		Registry:        reg,
		TrafficRegistry: trafficReg,
	})
	rolloutManager := rollout.NewManager(rollout.Config{
		ApplyManager:     applyManager,
		Store:            store,
		Metrics:          metrics,
		LockedBake:       20 * time.Millisecond,
		ErrorRateWindow:  500 * time.Millisecond,
		ErrorRatePercent: 50,
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
		Store:          store,
		ApplyManager:   applyManager,
		Auth:           auth,
		RateLimiter:    limiter,
		AdminStore:     adminStore,
		PublicKey:      keyPair.PublicKey,
		RolloutManager: rolloutManager,
	})
	adminServer := startAdminServer(t, adminHandler, adminTLS)
	defer adminServer.Close()

	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, metrics, nil, nil), Metrics: metrics}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := testutil.NewAdminClient(t, testutil.AdminClientConfig{CAFile: ca.CertFile, ClientCert: &clientCert, Token: "secret", ServerName: "admin.local"})
	if resp, _, err := client.PostJSON(adminServer.URL+"/admin/bundle", mustMarshalJSON(t, bundleA)); err != nil {
		t.Fatalf("bundle A request: %v", err)
	} else if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if resp, _, err := client.PostJSON(adminServer.URL+"/admin/bundle", mustMarshalJSON(t, bundleB)); err != nil {
		t.Fatalf("bundle B request: %v", err)
	} else if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	clientHTTP := &http.Client{Timeout: 2 * time.Second}
	resp, body := sendProxyRequest(t, clientHTTP, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if string(body) != "B" {
		t.Fatalf("expected B response, got %q", string(body))
	}

	rollbackPayload := []byte(`{"version":""}`)
	resp, _, err = client.PostJSON(adminServer.URL+"/admin/rollback", rollbackPayload)
	if err != nil {
		t.Fatalf("rollback request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	resp, body = sendProxyRequest(t, clientHTTP, proxyServer.URL, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if string(body) != "A" {
		t.Fatalf("expected A response, got %q", string(body))
	}

	metricsHandler := metrics.Handler()
	metricsServer := httptest.NewServer(metricsHandler)
	defer metricsServer.Close()
	metricsResp, err := metricsServer.Client().Get(metricsServer.URL)
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	metricsBody, err := io.ReadAll(metricsResp.Body)
	metricsResp.Body.Close()
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	count, ok := parseMetricCount(string(metricsBody), "proxy_rollback_total")
	if !ok || count < 1 {
		t.Fatalf("expected rollback metric, got %f", count)
	}
}

func mustMarshalJSON(t *testing.T, value interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return data
}
