package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestCanaryAutoDrain(t *testing.T) {
	traffic.SetSeedForTests(3)
	stable := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Variant", "stable")
		w.WriteHeader(http.StatusOK)
	})
	canary := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Variant", "canary")
		w.WriteHeader(http.StatusInternalServerError)
	})

	stableAddr, closeStable := testutil.StartUpstream(t, stable)
	defer closeStable()
	canaryAddr, closeCanary := testutil.StartUpstream(t, canary)
	defer closeCanary()

	reg := registry.NewRegistry(0, 0)
	trafficReg := traffic.NewRegistry(0, 0)

	cfg := &config.Config{
		Routes: []config.Route{
			{
				ID:         "r1",
				Host:       "example.local",
				PathPrefix: "/",
				Pool:       "pStable",
				Policy: config.RoutePolicy{
					Traffic: config.TrafficConfig{
						Enabled:      true,
						StablePool:   "pStable",
						CanaryPool:   "pCanary",
						StableWeight: 50,
						CanaryWeight: 50,
						AutoDrain: config.AutoDrainConfig{
							Enabled:             true,
							WindowMS:            500,
							MinRequests:         10,
							ErrorRateMultiplier: 2,
							CooloffMS:           1000,
						},
					},
				},
			},
		},
		Pools: map[string]config.Pool{
			"pStable": {Endpoints: []string{stableAddr}},
			"pCanary": {Endpoints: []string{canaryAddr}},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, nil, nil, nil)}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = oldStdout
	}()
	var logMu sync.Mutex
	logLines := []string{}
	logDone := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			logMu.Lock()
			logLines = append(logLines, line)
			logMu.Unlock()
		}
		close(logDone)
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	initialCounts := countVariantHeaders(t, client, proxyServer.URL, "example.local", "/", 50, nil)
	if initialCounts["canary"] == 0 {
		t.Fatalf("expected canary traffic before autodrain")
	}

	testutil.Eventually(t, 2*time.Second, 100*time.Millisecond, func() error {
		counts := countVariantHeaders(t, client, proxyServer.URL, "example.local", "/", 10, nil)
		if counts["canary"] <= 1 {
			return nil
		}
		return fmt.Errorf("autodrain not active yet")
	})

	postDrainCounts := countVariantHeaders(t, client, proxyServer.URL, "example.local", "/", 30, nil)
	if postDrainCounts["canary"] > 2 {
		t.Fatalf("expected canary drained, got counts %v", postDrainCounts)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	<-logDone
	logMu.Lock()
	lines := append([]string(nil), logLines...)
	logMu.Unlock()
	os.Stdout = oldStdout
	seenAutoDrain := false
	for _, line := range lines {
		if strings.Contains(line, "autodrain_active") {
			var payload map[string]interface{}
			if err := json.Unmarshal([]byte(line), &payload); err != nil {
				continue
			}
			if active, ok := payload["autodrain_active"].(bool); ok && active {
				seenAutoDrain = true
				break
			}
		}
	}
	if !seenAutoDrain {
		t.Fatalf("expected autodrain_active in logs")
	}

	time.Sleep(1100 * time.Millisecond)
	resumedCounts := countVariantHeaders(t, client, proxyServer.URL, "example.local", "/", 10, nil)
	if resumedCounts["canary"] == 0 {
		t.Fatalf("expected canary traffic after cooloff")
	}
}
