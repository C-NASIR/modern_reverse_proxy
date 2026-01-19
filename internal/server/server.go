package server

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"time"

	"modern_reverse_proxy/internal/runtime"
)

type Server struct {
	HTTPAddr string
	TLSAddr  string
	Close    func() error
}

func BaseTLSConfig(store *runtime.Store) *tls.Config {
	return &tls.Config{
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			snap := store.Get()
			if snap == nil || !snap.TLSEnabled || snap.TLSConfig == nil {
				log.Printf("tls config missing for incoming connection")
				return nil, errors.New("tls config missing")
			}
			return snap.TLSConfig, nil
		},
	}
}

func StartServers(handler http.Handler, tlsCfg *tls.Config, httpAddr string, tlsAddr string) (*Server, error) {
	if handler == nil {
		return nil, errors.New("handler is nil")
	}

	var httpSrv *http.Server
	var tlsSrv *http.Server
	var httpLn net.Listener
	var tlsLn net.Listener

	if httpAddr != "" {
		ln, err := net.Listen("tcp", httpAddr)
		if err != nil {
			return nil, err
		}
		httpLn = ln
		httpSrv = &http.Server{Handler: handler}
		go serve(httpSrv, httpLn)
	}

	if tlsAddr != "" {
		if tlsCfg == nil {
			if httpLn != nil {
				_ = httpLn.Close()
			}
			return nil, errors.New("tls config is required")
		}
		ln, err := net.Listen("tcp", tlsAddr)
		if err != nil {
			if httpLn != nil {
				_ = httpLn.Close()
			}
			return nil, err
		}
		tlsLn = ln
		tlsSrv = &http.Server{Handler: handler}
		go serve(tlsSrv, tls.NewListener(tlsLn, tlsCfg))
	}

	if httpLn == nil && tlsLn == nil {
		return nil, errors.New("no listeners configured")
	}

	closeFn := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var firstErr error
		if httpSrv != nil {
			if err := httpSrv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				firstErr = err
			}
		}
		if tlsSrv != nil {
			if err := tlsSrv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}

	return &Server{
		HTTPAddr: addrString(httpLn),
		TLSAddr:  addrString(tlsLn),
		Close:    closeFn,
	}, nil
}

func serve(server *http.Server, ln net.Listener) {
	if server == nil || ln == nil {
		return
	}
	if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("server error: %v", err)
	}
}

func addrString(ln net.Listener) string {
	if ln == nil {
		return ""
	}
	return ln.Addr().String()
}
