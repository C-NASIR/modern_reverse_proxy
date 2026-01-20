package integration

import (
	"net/http"
	"testing"
	"time"

	"modern_reverse_proxy/internal/testutil"
)

func TestShutdownDrainSequence(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})

	upstreamAddr, closeUpstream := testutil.StartUpstream(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-block
		writer.WriteHeader(http.StatusOK)
	}))
	defer closeUpstream()

	extra := `"shutdown": {"drain_ms": 100, "graceful_timeout_ms": 2000, "force_close_ms": 200}`
	cfgJSON := buildProxyConfig(upstreamAddr, extra)
	serverHandle, _, _, _ := startProxy(t, cfgJSON)

	client := &http.Client{Timeout: 2 * time.Second}
	responseCh := make(chan *http.Response, 1)
	responseErr := make(chan error, 1)

	go func() {
		resp, err := sendSimpleRequest(client, serverHandle.HTTPAddr, "example.local")
		if err != nil {
			responseErr <- err
			return
		}
		responseCh <- resp
	}()

	<-started
	shutdownErr := make(chan error, 1)
	go func() {
		shutdownErr <- serverHandle.Shutdown()
	}()

	resp2, err := sendSimpleRequest(client, serverHandle.HTTPAddr, "example.local")
	if resp2 != nil {
		resp2.Body.Close()
	}
	if err == nil && resp2.StatusCode == http.StatusOK {
		t.Fatalf("expected request to be rejected during shutdown")
	}

	close(block)
	select {
	case resp := <-responseCh:
		resp.Body.Close()
	case err := <-responseErr:
		t.Fatalf("inflight request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for inflight request")
	}

	select {
	case err := <-shutdownErr:
		if err != nil {
			t.Fatalf("shutdown error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("shutdown did not complete")
	}
}

func sendSimpleRequest(client *http.Client, addr string, host string) (*http.Response, error) {
	url := "http://" + addr + "/"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Host = host
	return client.Do(req)
}
