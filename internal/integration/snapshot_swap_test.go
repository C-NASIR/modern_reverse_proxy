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
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
)

func TestSnapshotSwapAtomicity(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})

	a1Handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-block
		_, _ = io.WriteString(w, "A1")
	})

	a1Addr, closeA1 := testutil.StartUpstream(t, a1Handler)
	defer closeA1()
	a2Addr, closeA2 := testutil.StartUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "A2")
	}))
	defer closeA2()

	b1Addr, closeB1 := testutil.StartUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "B1")
	}))
	defer closeB1()
	b2Addr, closeB2 := testutil.StartUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "B2")
	}))
	defer closeB2()

	initialConfig := fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{
"id": "r1",
"host": "example.local",
"path_prefix": "/",
"pool": "p1"
}],
"pools": {
"p1": { "endpoints": ["%s", "%s"] }
}
}`, a1Addr, a2Addr)

	cfg, err := config.ParseJSON([]byte(initialConfig))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	reg := registry.NewRegistry(0, 0)
	initialSnap, err := runtime.BuildSnapshot(cfg, reg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(initialSnap)
	engine := proxy.NewEngine(reg, nil, nil)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: engine}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	responseCh := make(chan string, 1)
	responseErr := make(chan error, 1)

	go func() {
		body, err := makeRequest(client, proxyServer.URL, "example.local")
		if err != nil {
			responseErr <- err
			return
		}
		responseCh <- body
	}()

	<-started

	nextConfig := fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{
"id": "r1",
"host": "example.local",
"path_prefix": "/",
"pool": "p1"
}],
"pools": {
"p1": { "endpoints": ["%s", "%s"] }
}
}`, b1Addr, b2Addr)

	nextCfg, err := config.ParseJSON([]byte(nextConfig))
	if err != nil {
		t.Fatalf("parse next config: %v", err)
	}
	nextSnap, err := runtime.BuildSnapshot(nextCfg, reg)
	if err != nil {
		t.Fatalf("build next snapshot: %v", err)
	}
	store.Swap(nextSnap)

	testutil.Eventually(t, time.Second, 20*time.Millisecond, func() error {
		body, err := makeRequest(client, proxyServer.URL, "example.local")
		if err != nil {
			return err
		}
		if strings.HasPrefix(body, "B") {
			return nil
		}
		return fmt.Errorf("expected B response, got %q", body)
	})

	close(block)
	select {
	case err := <-responseErr:
		t.Fatalf("blocked request failed: %v", err)
	case body := <-responseCh:
		if !strings.HasPrefix(body, "A") {
			t.Fatalf("expected in-flight A response, got %q", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for blocked response")
	}
}

func makeRequest(client *http.Client, baseURL, host string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL+"/", nil)
	if err != nil {
		return "", err
	}
	req.Host = host
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	return string(body), nil
}
