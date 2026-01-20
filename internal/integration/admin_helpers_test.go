package integration

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func newAdminTLSConfig(t *testing.T, certFile string, keyFile string, caFile string) *tls.Config {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("load server cert: %v", err)
	}
	caData, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatalf("read CA file: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caData) {
		t.Fatalf("append CA")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequestClientCert,
	}
}

func startAdminServer(t *testing.T, handler http.Handler, tlsConfig *tls.Config) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.TLS = tlsConfig
	server.StartTLS()
	return server
}
