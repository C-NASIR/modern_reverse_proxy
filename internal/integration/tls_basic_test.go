package integration

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/testutil"
)

func TestTLSBasicHandshake(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	serverCert := testutil.WriteSelfSignedCert(t, "example.local")
	cfg := &config.Config{
		TLS: config.TLSConfig{
			Enabled: true,
			Addr:    "127.0.0.1:0",
			Certs: []config.TLSCert{
				{ServerName: "example.local", CertFile: serverCert.CertFile, KeyFile: serverCert.KeyFile},
			},
		},
		Routes: []config.Route{
			{ID: "r1", Host: "example.local", PathPrefix: "/", Pool: "p1"},
		},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{addr}},
		},
	}

	proxyServer, _, _ := startTLSProxy(t, cfg)

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = oldStdout
	}()

	pool := x509CertPool(t, serverCert.Cert)
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "example.local"},
		},
	}
	resp, _ := sendProxyRequest(t, client, "https://"+proxyServer.TLSAddr, "example.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

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
	if payload["tls"] != true {
		t.Fatalf("expected tls true in log")
	}
}
