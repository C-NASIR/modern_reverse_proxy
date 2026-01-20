package integration

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"modern_reverse_proxy/internal/apply"
	"modern_reverse_proxy/internal/bundle"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/distributor"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/pull"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/rollout"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestDistributorPullApplySwapsSnapshot(t *testing.T) {
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
		Source:    "distributor",
	}
	bundleA, err := bundle.NewSignedBundle([]byte(configA), metaA, keyPair.PrivateKey)
	if err != nil {
		t.Fatalf("bundle A sign: %v", err)
	}
	metaB := bundle.Meta{
		Version:   apply.ConfigVersion([]byte(configB)),
		CreatedAt: time.Now().Add(10 * time.Millisecond).UTC().Format(time.RFC3339Nano),
		Source:    "distributor",
	}
	bundleB, err := bundle.NewSignedBundle([]byte(configB), metaB, keyPair.PrivateKey)
	if err != nil {
		t.Fatalf("bundle B sign: %v", err)
	}

	storage := bundle.NewMemoryStorage()
	if err := storage.Put(bundleA); err != nil {
		t.Fatalf("store bundle A: %v", err)
	}

	distributorHandler := distributor.NewHandler(distributor.Config{Storage: storage})
	distributorServer := httptest.NewServer(distributorHandler)
	defer distributorServer.Close()

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
	puller := pull.NewPuller(pull.Config{
		Enabled:        true,
		BaseURL:        distributorServer.URL,
		Interval:       20 * time.Millisecond,
		Jitter:         0,
		PublicKey:      keyPair.PublicKey,
		RolloutManager: rolloutManager,
		Store:          store,
		HTTPClient:     distributorServer.Client(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go puller.Run(ctx)

	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, metrics, nil, nil), Metrics: metrics}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	testutil.Eventually(t, time.Second, 20*time.Millisecond, func() error {
		resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("expected 200, got %d", resp.StatusCode)
		}
		if string(body) != "A" {
			return fmt.Errorf("expected A response, got %q", string(body))
		}
		return nil
	})

	if err := storage.Put(bundleB); err != nil {
		t.Fatalf("store bundle B: %v", err)
	}

	testutil.Eventually(t, time.Second, 20*time.Millisecond, func() error {
		resp, body := sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("expected 200, got %d", resp.StatusCode)
		}
		if string(body) != "B" {
			return fmt.Errorf("expected B response, got %q", string(body))
		}
		return nil
	})
}
