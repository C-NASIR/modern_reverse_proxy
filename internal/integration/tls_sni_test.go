package integration

import (
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/testutil"
)

func TestTLSSNISelectsCorrectCertificate(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	certA := testutil.WriteSelfSignedCert(t, "a.example.local")
	certB := testutil.WriteSelfSignedCert(t, "b.example.local")

	cfg := &config.Config{
		TLS: config.TLSConfig{
			Enabled: true,
			Addr:    "127.0.0.1:0",
			Certs: []config.TLSCert{
				{ServerName: "a.example.local", CertFile: certA.CertFile, KeyFile: certA.KeyFile},
				{ServerName: "b.example.local", CertFile: certB.CertFile, KeyFile: certB.KeyFile},
			},
		},
		Routes: []config.Route{
			{ID: "a", Host: "a.example.local", PathPrefix: "/", Pool: "p1"},
			{ID: "b", Host: "b.example.local", PathPrefix: "/", Pool: "p1"},
		},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{addr}},
		},
	}

	proxyServer, _, _ := startTLSProxy(t, cfg)

	pool := x509CertPool(t, certA.Cert, certB.Cert)
	assertPeerName := func(serverName string) {
		client := &http.Client{
			Timeout: 2 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: serverName},
			},
		}
		resp, _ := sendProxyRequest(t, client, "https://"+proxyServer.TLSAddr, serverName, http.MethodGet, "/")
		if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
			t.Fatalf("missing peer certificate")
		}
		if resp.TLS.PeerCertificates[0].Subject.CommonName != serverName {
			t.Fatalf("expected cert %s, got %s", serverName, resp.TLS.PeerCertificates[0].Subject.CommonName)
		}
	}

	assertPeerName("a.example.local")
	assertPeerName("b.example.local")
}
