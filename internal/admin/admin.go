package admin

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"

	"modern_reverse_proxy/internal/apply"
	"modern_reverse_proxy/internal/rollout"
	"modern_reverse_proxy/internal/runtime"
)

type HandlerConfig struct {
	Store          *runtime.Store
	ApplyManager   *apply.Manager
	Auth           *Authenticator
	RateLimiter    *RateLimiter
	AdminStore     *Store
	PublicKey      ed25519.PublicKey
	AllowUnsigned  bool
	RolloutManager *rollout.Manager
}

func NewHandler(cfg HandlerConfig) http.Handler {
	h := &handler{
		store:         cfg.Store,
		apply:         cfg.ApplyManager,
		auth:          cfg.Auth,
		rateLimiter:   cfg.RateLimiter,
		adminStore:    cfg.AdminStore,
		publicKey:     cfg.PublicKey,
		allowUnsigned: cfg.AllowUnsigned,
		rollout:       cfg.RolloutManager,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/validate", h.handleValidate)
	mux.HandleFunc("/admin/config", h.handleApply)
	mux.HandleFunc("/admin/bundle", h.handleBundle)
	mux.HandleFunc("/admin/rollback", h.handleRollback)
	mux.HandleFunc("/admin/snapshot", h.handleSnapshot)
	h.mux = mux
	return h
}

func TLSConfig(certFile string, keyFile string, clientCAFile string) (*tls.Config, error) {
	if certFile == "" || keyFile == "" {
		return nil, errors.New("admin cert and key are required")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	var pool *x509.CertPool
	if clientCAFile != "" {
		caData, err := os.ReadFile(clientCAFile)
		if err != nil {
			return nil, err
		}
		pool = x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caData) {
			return nil, errors.New("failed to parse client CA")
		}
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequestClientCert,
		NextProtos:   []string{"h2", "http/1.1"},
	}, nil
}
