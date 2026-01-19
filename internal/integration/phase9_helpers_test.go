package integration

import (
	"io"
	"net/http"
	"testing"
)

func sendProxyRequestWithHeaders(t *testing.T, client *http.Client, baseURL, host, method, path string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, baseURL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = host
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body.Close()
		t.Fatalf("read body: %v", err)
	}
	resp.Body.Close()
	return resp, body
}

func countVariantHeaders(t *testing.T, client *http.Client, baseURL, host, path string, requests int, headers map[string]string) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for i := 0; i < requests; i++ {
		resp, _ := sendProxyRequestWithHeaders(t, client, baseURL, host, http.MethodGet, path, headers)
		variant := resp.Header.Get("X-Variant")
		if variant == "" {
			variant = "none"
		}
		counts[variant]++
	}
	return counts
}
