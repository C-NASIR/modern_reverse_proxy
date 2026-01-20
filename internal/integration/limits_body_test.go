package integration

import (
	"bytes"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"modern_reverse_proxy/internal/testutil"
)

func TestBodySizeLimit(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstreamAddr, closeUpstream := testutil.StartUpstream(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamCalls.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer closeUpstream()

	cfgJSON := buildProxyConfig(upstreamAddr, `"limits": {"max_body_bytes": 100}`)
	serverHandle, _, _, _ := startProxy(t, cfgJSON)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, body := sendProxyRequestWithBody(t, client, serverHandle.HTTPAddr, "example.local", bytes.Repeat([]byte("a"), 1000))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", resp.StatusCode)
	}
	assertProxyError(t, resp, body, "request_too_large")
	if upstreamCalls.Load() != 0 {
		t.Fatalf("expected upstream not called")
	}

	resp, body = sendProxyRequestChunked(t, client, serverHandle.HTTPAddr, "example.local", bytes.Repeat([]byte("b"), 200))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", resp.StatusCode)
	}
	assertProxyError(t, resp, body, "request_too_large")
	if upstreamCalls.Load() != 0 {
		t.Fatalf("expected upstream not called")
	}
}

func sendProxyRequestWithBody(t *testing.T, client *http.Client, addr string, host string, body []byte) (*http.Response, []byte) {
	t.Helper()
	url := "http://" + addr + "/"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = host
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body.Close()
		t.Fatalf("read body: %v", err)
	}
	resp.Body.Close()
	return resp, respBody
}

func sendProxyRequestChunked(t *testing.T, client *http.Client, addr string, host string, body []byte) (*http.Response, []byte) {
	t.Helper()
	url := "http://" + addr + "/"
	reader, writer := io.Pipe()
	req, err := http.NewRequest(http.MethodPost, url, reader)
	if err != nil {
		writer.Close()
		t.Fatalf("new request: %v", err)
	}
	req.Host = host
	req.ContentLength = -1

	go func() {
		_, _ = writer.Write(body)
		writer.Close()
	}()

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body.Close()
		t.Fatalf("read body: %v", err)
	}
	resp.Body.Close()
	return resp, respBody
}
