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

func TestMTLSRouteEnforcement(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	serverCert := testutil.WriteSelfSignedCert(t, "secure.local")
	clientCA := testutil.WriteCA(t, "client-ca")
	clientCert := testutil.WriteClientCert(t, "client", clientCA)

	cfg := &config.Config{
		TLS: config.TLSConfig{
			Enabled:      true,
			Addr:         "127.0.0.1:0",
			ClientCAFile: clientCA.CertFile,
			Certs: []config.TLSCert{
				{ServerName: "secure.local", CertFile: serverCert.CertFile, KeyFile: serverCert.KeyFile},
			},
		},
		Routes: []config.Route{
			{ID: "secure", Host: "secure.local", PathPrefix: "/", Pool: "p1", Policy: config.RoutePolicy{RequireMTLS: true}},
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

	rootPool := x509CertPool(t, serverCert.Cert)
	client1 := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: rootPool, ServerName: "secure.local"},
		},
	}
	resp, body := sendProxyRequest(t, client1, "https://"+proxyServer.TLSAddr, "secure.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
	assertProxyError(t, resp, body, "mtls_required")

	clientPair, err := tls.LoadX509KeyPair(clientCert.CertFile, clientCert.KeyFile)
	if err != nil {
		t.Fatalf("load client cert: %v", err)
	}
	client2 := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      rootPool,
				ServerName:   "secure.local",
				Certificates: []tls.Certificate{clientPair},
			},
		},
	}
	resp, _ = sendProxyRequest(t, client2, "https://"+proxyServer.TLSAddr, "secure.local", http.MethodGet, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	lines := readLines(t, reader)
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 log lines, got %d", len(lines))
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &payload); err != nil {
		t.Fatalf("parse log json: %v", err)
	}
	if payload["mtls_verified"] != true {
		t.Fatalf("expected mtls_verified true in log")
	}
}
