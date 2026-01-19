package integration

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestTLSReloadSwap(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	addr, closeUpstream := testutil.StartUpstream(t, upstream)
	defer closeUpstream()

	cert1 := testutil.WriteSelfSignedCert(t, "reload.local")
	cert2 := testutil.WriteSelfSignedCert(t, "reload.local")

	cfg1 := &config.Config{
		TLS: config.TLSConfig{
			Enabled: true,
			Addr:    "127.0.0.1:0",
			Certs:   []config.TLSCert{{ServerName: "reload.local", CertFile: cert1.CertFile, KeyFile: cert1.KeyFile}},
		},
		Routes: []config.Route{
			{ID: "r1", Host: "reload.local", PathPrefix: "/", Pool: "p1"},
		},
		Pools: map[string]config.Pool{
			"p1": {Endpoints: []string{addr}},
		},
	}

	proxyServer, store, reg := startTLSProxy(t, cfg1)

	pool := x509CertPool(t, cert1.Cert, cert2.Cert)
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "reload.local"},
		},
	}
	resp, _ := sendProxyRequest(t, client, "https://"+proxyServer.TLSAddr, "reload.local", http.MethodGet, "/")
	firstFingerprint := fingerprintCert(t, resp)

	cfg2 := &config.Config{
		TLS: config.TLSConfig{
			Enabled: true,
			Addr:    proxyServer.TLSAddr,
			Certs:   []config.TLSCert{{ServerName: "reload.local", CertFile: cert2.CertFile, KeyFile: cert2.KeyFile}},
		},
		Routes: cfg1.Routes,
		Pools:  cfg1.Pools,
	}

	trafficReg := traffic.NewRegistry(0, 0)
	snap2, err := runtime.BuildSnapshot(cfg2, reg, nil, nil, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot2: %v", err)
	}
	store.Swap(snap2)

	client = &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSClientConfig:   &tls.Config{RootCAs: pool, ServerName: "reload.local"},
		},
	}
	resp, _ = sendProxyRequest(t, client, "https://"+proxyServer.TLSAddr, "reload.local", http.MethodGet, "/")
	secondFingerprint := fingerprintCert(t, resp)

	if firstFingerprint == secondFingerprint {
		t.Fatalf("expected certificate to change after swap")
	}
}

func fingerprintCert(t *testing.T, resp *http.Response) string {
	t.Helper()
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		t.Fatalf("missing peer certificate")
	}
	sum := sha256.Sum256(resp.TLS.PeerCertificates[0].Raw)
	return hex.EncodeToString(sum[:])
}
