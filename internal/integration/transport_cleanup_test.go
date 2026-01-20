package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/pool"
	"modern_reverse_proxy/internal/runtime"
)

func TestTransportCleanupOnPoolRemoval(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	endpoint := strings.TrimPrefix(upstream.URL, "http://")
	configJSON := fmt.Sprintf(`{
		"listen_addr": "127.0.0.1:0",
		"routes": [
			{
				"id": "r1",
				"host": "example.com",
				"path_prefix": "/",
				"pool": "p1",
				"policy": {}
			}
		],
		"pools": {
			"p1": {
				"endpoints": ["%s"]
			}
		}
	}`, endpoint)

	serverHandle, store, reg, trafficReg := startProxy(t, configJSON)
	reg.SetTransportTTL(100 * time.Millisecond)

	client := &http.Client{}
	req, err := http.NewRequest(http.MethodGet, "http://"+serverHandle.HTTPAddr+"/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "example.com"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if !reg.HasTransport(pool.PoolKey("p1")) {
		t.Fatalf("expected transport for pool p1")
	}

	updatedConfig, err := config.ParseJSON([]byte(`{
		"listen_addr": "127.0.0.1:0",
		"routes": [],
		"pools": {}
	}`))
	if err != nil {
		t.Fatalf("parse updated config: %v", err)
	}
	updatedSnap, err := runtime.BuildSnapshot(updatedConfig, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build updated snapshot: %v", err)
	}
	if err := store.Swap(updatedSnap); err != nil {
		t.Fatalf("swap snapshot: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		reg.ReapTransportsNow()
		if !reg.HasTransport(pool.PoolKey("p1")) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("transport for pool p1 not cleaned up")
}
