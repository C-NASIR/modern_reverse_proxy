package admin

import (
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"strings"
)

type AuthConfig struct {
	Token        string
	ClientCAFile string
}

type Authenticator struct {
	token     string
	clientCAs *x509.CertPool
}

type AuthError struct {
	Status  int
	Message string
}

func (e *AuthError) Error() string {
	if e == nil {
		return "auth error"
	}
	return e.Message
}

func NewAuthenticator(cfg AuthConfig) (*Authenticator, error) {
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return nil, errors.New("admin token is required")
	}

	var pool *x509.CertPool
	if cfg.ClientCAFile != "" {
		caData, err := os.ReadFile(cfg.ClientCAFile)
		if err != nil {
			return nil, err
		}
		pool = x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caData) {
			return nil, errors.New("failed to parse client CA")
		}
	}

	return &Authenticator{token: token, clientCAs: pool}, nil
}

func (a *Authenticator) Authenticate(r *http.Request) error {
	if a == nil {
		return &AuthError{Status: http.StatusUnauthorized, Message: "auth unavailable"}
	}
	if r == nil || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return &AuthError{Status: http.StatusForbidden, Message: "client certificate required"}
	}
	if a.clientCAs != nil {
		cert := r.TLS.PeerCertificates[0]
		intermediates := x509.NewCertPool()
		for _, chainCert := range r.TLS.PeerCertificates[1:] {
			intermediates.AddCert(chainCert)
		}
		if _, err := cert.Verify(x509.VerifyOptions{
			Roots:         a.clientCAs,
			Intermediates: intermediates,
			KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}); err != nil {
			return &AuthError{Status: http.StatusForbidden, Message: "client certificate invalid"}
		}
	}

	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok || token == "" {
		return &AuthError{Status: http.StatusUnauthorized, Message: "token required"}
	}
	if token != a.token {
		return &AuthError{Status: http.StatusUnauthorized, Message: "token invalid"}
	}
	return nil
}

func bearerToken(header string) (string, bool) {
	if header == "" {
		return "", false
	}
	parts := strings.Fields(header)
	if len(parts) != 2 {
		return "", false
	}
	if !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	return parts[1], true
}
