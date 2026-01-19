package integration

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
)

func TestAccessLogContainsRequiredFields(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "A")
		_, _ = io.WriteString(w, "ok")
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	reg := registry.NewRegistry(50*time.Millisecond, 200*time.Millisecond)
	defer reg.Close()

	metrics := obs.NewMetrics(obs.MetricsConfig{})
	obs.SetDefaultMetrics(metrics)
	defer obs.SetDefaultMetrics(nil)

	cfg := &config.Config{
		Routes: []config.Route{
			{
				ID:         "r1",
				Host:       "example.local",
				PathPrefix: "/",
				Pool:       "p1",
			},
		},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{addr}},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, reg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, metrics), Metrics: metrics}
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

	client := &http.Client{Timeout: 2 * time.Second}
	_, _ = sendProxyRequest(t, client, proxyServer.URL, "example.local", http.MethodGet, "/")

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	lines := readLines(t, reader)
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &payload); err != nil {
		t.Fatalf("parse log json: %v", err)
	}

	assertLogField(t, payload, "request_id")
	if payload["method"] != "GET" {
		t.Fatalf("expected method GET")
	}
	if payload["host"] != "example.local" {
		t.Fatalf("expected host example.local")
	}
	if payload["path"] != "/" {
		t.Fatalf("expected path /")
	}
	if payload["route_id"] != "r1" {
		t.Fatalf("expected route_id r1")
	}
	if payload["upstream_addr"] != addr {
		t.Fatalf("expected upstream addr %q", addr)
	}
	if payload["error_category"] != "none" {
		t.Fatalf("expected error_category none")
	}
	assertLogField(t, payload, "status")
	assertLogField(t, payload, "duration_ms")
	assertLogField(t, payload, "snapshot_version")
}

func readLines(t *testing.T, r *os.File) []string {
	t.Helper()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read log data: %v", err)
	}
	lines := []string{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan logs: %v", err)
	}
	return lines
}

func assertLogField(t *testing.T, payload map[string]interface{}, field string) {
	t.Helper()
	value, ok := payload[field]
	if !ok {
		t.Fatalf("missing log field %s", field)
	}
	switch v := value.(type) {
	case string:
		if v == "" {
			t.Fatalf("log field %s is empty", field)
		}
	}
}
