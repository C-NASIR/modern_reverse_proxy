package tlsstore

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strings"
)

type Store struct {
	certs       map[string]*tls.Certificate
	defaultCert *tls.Certificate
	clientCA    *x509.CertPool
}

func NewStore(certs map[string]*tls.Certificate, defaultCert *tls.Certificate, clientCA *x509.CertPool) *Store {
	return &Store{certs: certs, defaultCert: defaultCert, clientCA: clientCA}
}

func (s *Store) GetCertificate(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if s == nil {
		return nil, errors.New("tls store is nil")
	}
	if chi != nil && chi.ServerName != "" {
		if cert, ok := s.certs[strings.ToLower(chi.ServerName)]; ok {
			return cert, nil
		}
	}
	if s.defaultCert == nil {
		return nil, errors.New("default certificate missing")
	}
	return s.defaultCert, nil
}

func (s *Store) VerifyClientCert(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	if s == nil || s.clientCA == nil {
		return errors.New("client CA pool missing")
	}
	if len(rawCerts) == 0 {
		return errors.New("client certificate missing")
	}

	certs := make([]*x509.Certificate, len(rawCerts))
	for i, raw := range rawCerts {
		cert, err := x509.ParseCertificate(raw)
		if err != nil {
			return err
		}
		certs[i] = cert
	}

	intermediates := x509.NewCertPool()
	for _, cert := range certs[1:] {
		intermediates.AddCert(cert)
	}

	_, err := certs[0].Verify(x509.VerifyOptions{
		Roots:         s.clientCA,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	return err
}
