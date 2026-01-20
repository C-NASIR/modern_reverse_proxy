package testutil

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"os"
	"testing"
)

type AdminClient struct {
	Client *http.Client
	Token  string
}

type AdminClientConfig struct {
	CAFile     string
	ClientCert *CertFiles
	Token      string
	ServerName string
}

func NewAdminClient(t *testing.T, cfg AdminClientConfig) *AdminClient {
	t.Helper()

	rootPool := x509.NewCertPool()
	if cfg.CAFile != "" {
		caData, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			t.Fatalf("read CA file: %v", err)
		}
		if !rootPool.AppendCertsFromPEM(caData) {
			t.Fatalf("append CA cert")
		}
	}

	tlsConfig := &tls.Config{
		RootCAs: rootPool,
	}
	if cfg.ServerName != "" {
		tlsConfig.ServerName = cfg.ServerName
	}
	if cfg.ClientCert != nil {
		cert, err := tls.LoadX509KeyPair(cfg.ClientCert.CertFile, cfg.ClientCert.KeyFile)
		if err != nil {
			t.Fatalf("load client cert: %v", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	return &AdminClient{Client: client, Token: cfg.Token}
}

func (c *AdminClient) Do(req *http.Request) (*http.Response, error) {
	if c == nil || c.Client == nil {
		return nil, http.ErrServerClosed
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return c.Client.Do(req)
}

func (c *AdminClient) PostJSON(url string, body []byte) (*http.Response, []byte, error) {
	if c == nil {
		return nil, nil, http.ErrServerClosed
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, err
	}
	return resp, data, nil
}
