package server

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"modern_reverse_proxy/internal/limits"
	"modern_reverse_proxy/internal/runtime"
)

type Server struct {
	HTTPAddr string
	TLSAddr  string

	httpServer   *http.Server
	tlsServer    *http.Server
	httpLn       net.Listener
	tlsLn        net.Listener
	limits       limits.Limits
	shutdown     runtime.ShutdownConfig
	inflight     *runtime.InflightTracker
	stoppers     []Stopper
	closeIdle    []func()
	shutdownOnce sync.Once
	shutdownErr  error
}

type Stopper interface {
	Stop(ctx context.Context) error
}

type StopFunc func(ctx context.Context) error

func (s StopFunc) Stop(ctx context.Context) error {
	return s(ctx)
}

type Options struct {
	Limits    limits.Limits
	Shutdown  runtime.ShutdownConfig
	Inflight  *runtime.InflightTracker
	Stoppers  []Stopper
	CloseIdle []func()
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

func StartServers(handler http.Handler, tlsCfg *tls.Config, httpAddr string, tlsAddr string, options Options) (*Server, error) {
	if handler == nil {
		return nil, errors.New("handler is nil")
	}

	limitConfig := options.Limits
	if limitConfig.MaxHeaderBytes == 0 {
		limitConfig = limits.Default()
	}
	shutdownConfig := runtime.ApplyShutdownDefaults(options.Shutdown)

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
		httpSrv = &http.Server{
			Handler:           handler,
			MaxHeaderBytes:    limitConfig.MaxHeaderBytes,
			ReadHeaderTimeout: limitConfig.ReadHeaderTimeout,
			ReadTimeout:       limitConfig.ReadTimeout,
			WriteTimeout:      limitConfig.WriteTimeout,
			IdleTimeout:       limitConfig.IdleTimeout,
		}
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
		tlsSrv = &http.Server{
			Handler:           handler,
			MaxHeaderBytes:    limitConfig.MaxHeaderBytes,
			ReadHeaderTimeout: limitConfig.ReadHeaderTimeout,
			ReadTimeout:       limitConfig.ReadTimeout,
			WriteTimeout:      limitConfig.WriteTimeout,
			IdleTimeout:       limitConfig.IdleTimeout,
		}
		go serve(tlsSrv, tls.NewListener(tlsLn, tlsCfg))
	}

	if httpLn == nil && tlsLn == nil {
		return nil, errors.New("no listeners configured")
	}

	return &Server{
		HTTPAddr:   addrString(httpLn),
		TLSAddr:    addrString(tlsLn),
		httpServer: httpSrv,
		tlsServer:  tlsSrv,
		httpLn:     httpLn,
		tlsLn:      tlsLn,
		limits:     limitConfig,
		shutdown:   shutdownConfig,
		inflight:   options.Inflight,
		stoppers:   options.Stoppers,
		closeIdle:  options.CloseIdle,
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

func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	return s.Shutdown()
}

func (s *Server) Shutdown() error {
	if s == nil {
		return nil
	}
	s.shutdownOnce.Do(func() {
		s.shutdownErr = s.shutdownSequence()
	})
	return s.shutdownErr
}

func (s *Server) shutdownSequence() error {
	s.closeListeners()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), s.shutdown.GracefulTimeout)
	for _, stopper := range s.stoppers {
		if stopper == nil {
			continue
		}
		_ = stopper.Stop(stopCtx)
	}
	stopCancel()

	if s.shutdown.Drain > 0 {
		time.Sleep(s.shutdown.Drain)
	}

	for _, closeIdle := range s.closeIdle {
		if closeIdle != nil {
			closeIdle()
		}
	}

	gracefulCtx, gracefulCancel := context.WithTimeout(context.Background(), s.shutdown.GracefulTimeout)
	defer gracefulCancel()
	if s.inflight != nil {
		_ = s.inflight.Wait(gracefulCtx)
	}
	var firstErr error
	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(gracefulCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			firstErr = err
		}
	}
	if s.tlsServer != nil {
		if err := s.tlsServer.Shutdown(gracefulCtx); err != nil && !errors.Is(err, http.ErrServerClosed) && firstErr == nil {
			firstErr = err
		}
	}
	if gracefulCtx.Err() == nil {
		return firstErr
	}

	if s.shutdown.ForceClose > 0 {
		time.Sleep(s.shutdown.ForceClose)
	}
	s.closeServers()
	if firstErr != nil {
		return firstErr
	}
	return gracefulCtx.Err()
}

func (s *Server) closeListeners() {
	if s.httpLn != nil {
		_ = s.httpLn.Close()
	}
	if s.tlsLn != nil {
		_ = s.tlsLn.Close()
	}
}

func (s *Server) closeServers() {
	if s.httpServer != nil {
		_ = s.httpServer.Close()
	}
	if s.tlsServer != nil {
		_ = s.tlsServer.Close()
	}
}
