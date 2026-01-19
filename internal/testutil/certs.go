package testutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type CA struct {
	Cert     *x509.Certificate
	Key      *ecdsa.PrivateKey
	CertFile string
}

type CertFiles struct {
	Cert     *x509.Certificate
	CertFile string
	KeyFile  string
}

func WriteSelfSignedCert(t *testing.T, serverName string) CertFiles {
	t.Helper()
	key := generateKey(t)
	template := baseTemplate(serverName, []string{serverName}, true)
	template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	template.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign

	der := createCertificate(t, template, template, &key.PublicKey, key)
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := marshalKey(t, key)

	return CertFiles{
		Cert:     cert,
		CertFile: writeTempFile(t, "cert.pem", certPEM),
		KeyFile:  writeTempFile(t, "key.pem", keyPEM),
	}
}

func WriteCA(t *testing.T, commonName string) CA {
	t.Helper()
	key := generateKey(t)
	template := baseTemplate(commonName, nil, true)
	template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign

	der := createCertificate(t, template, template, &key.PublicKey, key)
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return CA{
		Cert:     cert,
		Key:      key,
		CertFile: writeTempFile(t, "ca.pem", certPEM),
	}
}

func WriteClientCert(t *testing.T, commonName string, ca CA) CertFiles {
	t.Helper()
	key := generateKey(t)
	template := baseTemplate(commonName, nil, false)
	template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	template.KeyUsage = x509.KeyUsageDigitalSignature

	der := createCertificate(t, template, ca.Cert, &key.PublicKey, ca.Key)
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse client cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := marshalKey(t, key)

	return CertFiles{
		Cert:     cert,
		CertFile: writeTempFile(t, "client.pem", certPEM),
		KeyFile:  writeTempFile(t, "client.key", keyPEM),
	}
}

func generateKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func createCertificate(t *testing.T, template *x509.Certificate, parent *x509.Certificate, pub *ecdsa.PublicKey, signer *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, template, parent, pub, signer)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return der
}

func marshalKey(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	data, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: data})
}

func writeTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func baseTemplate(commonName string, dnsNames []string, isCA bool) *x509.Certificate {
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		serial = big.NewInt(time.Now().UnixNano())
	}
	return &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: commonName,
		},
		DNSNames:              dnsNames,
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  isCA,
		BasicConstraintsValid: true,
	}
}
