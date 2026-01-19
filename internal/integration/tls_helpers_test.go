package integration

import (
	"crypto/x509"
	"net/http"
	"testing"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/server"
)

func startTLSProxy(t *testing.T, cfg *config.Config) (*server.Server, *runtime.Store, *registry.Registry) {
	t.Helper()
	reg := registry.NewRegistry(0, 0)

	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil)
	if err != nil {
		reg.Close()
		t.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(snap)
	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, nil, nil, nil)}
	mux := http.NewServeMux()
	mux.Handle("/", proxyHandler)

	serverHandle, err := server.StartServers(mux, server.BaseTLSConfig(store), "", snap.TLSAddr)
	if err != nil {
		reg.Close()
		t.Fatalf("start tls server: %v", err)
	}

	t.Cleanup(func() {
		_ = serverHandle.Close()
		reg.Close()
	})

	return serverHandle, store, reg
}

func x509CertPool(t *testing.T, certs ...*x509.Certificate) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	for _, cert := range certs {
		if cert == nil {
			continue
		}
		pool.AddCert(cert)
	}
	return pool
}
