package bench

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"modern_reverse_proxy/internal/config"
)

func BenchmarkProxyGET(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	endpoint := strings.TrimPrefix(upstream.URL, "http://")
	cfg := buildBaseConfig(endpoint)
	server, client, cleanup := startBenchmarkProxy(b, cfg, nil)
	defer cleanup()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, err := buildRequest(server.URL + "/")
		if err != nil {
			b.Fatalf("request: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			b.Fatalf("proxy request: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

func BenchmarkCacheHit(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("cached"))
	}))
	defer upstream.Close()

	endpoint := strings.TrimPrefix(upstream.URL, "http://")
	cfg := buildBaseConfig(endpoint)
	addCachePolicy(cfg)
	cacheLayer := newCacheLayer()
	server, client, cleanup := startBenchmarkProxy(b, cfg, cacheLayer)
	defer cleanup()

	warmReq, err := buildRequest(server.URL + "/")
	if err != nil {
		b.Fatalf("warm request: %v", err)
	}
	warmResp, err := client.Do(warmReq)
	if err != nil {
		b.Fatalf("warm request: %v", err)
	}
	_, _ = io.Copy(io.Discard, warmResp.Body)
	_ = warmResp.Body.Close()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, err := buildRequest(server.URL + "/")
		if err != nil {
			b.Fatalf("request: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			b.Fatalf("proxy request: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

func BenchmarkRetryDialFail(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	endpoint := strings.TrimPrefix(upstream.URL, "http://")
	cfg := buildBaseConfig(endpoint)
	cfg.Pools["p1"] = config.Pool{Endpoints: []string{"127.0.0.1:1", endpoint}}
	addRetryPolicy(cfg)
	server, client, cleanup := startBenchmarkProxy(b, cfg, nil)
	defer cleanup()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, err := buildRequest(server.URL + "/")
		if err != nil {
			b.Fatalf("request: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			b.Fatalf("proxy request: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}
