package tlsstore

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
)

type CertSpec struct {
	ServerName string
	CertFile   string
	KeyFile    string
}

func LoadStore(certs []CertSpec, clientCAFile string) (*Store, error) {
	if len(certs) == 0 {
		return nil, errors.New("no certificates configured")
	}

	loaded := make([]tls.Certificate, len(certs))
	certMap := make(map[string]*tls.Certificate, len(certs))
	for i, spec := range certs {
		if spec.ServerName == "" {
			return nil, errors.New("certificate server name is required")
		}
		if spec.CertFile == "" || spec.KeyFile == "" {
			return nil, fmt.Errorf("certificate files missing for %s", spec.ServerName)
		}
		cert, err := tls.LoadX509KeyPair(spec.CertFile, spec.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load cert for %s: %w", spec.ServerName, err)
		}
		loaded[i] = cert
		name := strings.ToLower(spec.ServerName)
		certMap[name] = &loaded[i]
	}

	var clientCA *x509.CertPool
	if clientCAFile != "" {
		data, err := os.ReadFile(clientCAFile)
		if err != nil {
			return nil, fmt.Errorf("read client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, errors.New("parse client CA")
		}
		clientCA = pool
	}

	return NewStore(certMap, &loaded[0], clientCA), nil
}
