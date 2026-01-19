package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"modern_reverse_proxy/internal/policy"
	"modern_reverse_proxy/internal/pool"
	"modern_reverse_proxy/internal/registry"
)

type Engine struct {
	mu         sync.Mutex
	transports map[string]*http.Transport
	registry   *registry.Registry
}

func NewEngine(reg *registry.Registry) *Engine {
	return &Engine{transports: make(map[string]*http.Transport), registry: reg}
}

func (e *Engine) Forward(w http.ResponseWriter, r *http.Request, poolKey pool.PoolKey, upstreamAddr string, policy policy.Policy, requestID string) {
	if upstreamAddr == "" {
		WriteProxyError(w, requestID, http.StatusBadGateway, "bad_gateway", "no upstream available")
		return
	}

	transport, err := e.transportFor(policy)
	if err != nil {
		WriteProxyError(w, requestID, http.StatusBadGateway, "bad_gateway", "upstream transport unavailable")
		return
	}

	if e.registry != nil {
		e.registry.InflightStart(poolKey, upstreamAddr)
		defer e.registry.InflightDone(poolKey, upstreamAddr)
	}

	target := &url.URL{
		Scheme:   "http",
		Host:     upstreamAddr,
		Path:     r.URL.Path,
		RawQuery: r.URL.RawQuery,
	}

	outbound, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		WriteProxyError(w, requestID, http.StatusBadGateway, "bad_gateway", "failed to create upstream request")
		return
	}

	outbound.Header = r.Header.Clone()
	outbound.Host = upstreamAddr
	setForwardedHeaders(outbound, r)

	resp, err := transport.RoundTrip(outbound)
	if err != nil {
		if isRequestTimeout(r.Context()) {
			WriteProxyError(w, requestID, http.StatusGatewayTimeout, "request_timeout", "request timed out")
			return
		}
		if isClientCanceled(r.Context()) {
			return
		}
		if isTimeoutError(err) {
			e.passiveFailure(poolKey, upstreamAddr)
			WriteProxyError(w, requestID, http.StatusGatewayTimeout, "upstream_timeout", "upstream timeout")
			return
		}
		if isDialError(err) {
			e.passiveFailure(poolKey, upstreamAddr)
			WriteProxyError(w, requestID, http.StatusBadGateway, "upstream_connect_failed", "upstream connect failed")
			return
		}
		WriteProxyError(w, requestID, http.StatusBadGateway, "bad_gateway", "upstream request failed")
		return
	}
	defer resp.Body.Close()
	e.passiveSuccess(poolKey, upstreamAddr)

	copyHeaders(w.Header(), resp.Header)
	w.Header().Set(RequestIDHeader, requestID)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func setForwardedHeaders(outbound *http.Request, inbound *http.Request) {
	clientIP := inbound.RemoteAddr
	if host, _, err := net.SplitHostPort(inbound.RemoteAddr); err == nil {
		clientIP = host
	}

	if clientIP != "" {
		prior := outbound.Header.Get("X-Forwarded-For")
		if prior != "" {
			clientIP = prior + ", " + clientIP
		}
		outbound.Header.Set("X-Forwarded-For", clientIP)
	}

	proto := "http"
	if inbound.TLS != nil {
		proto = "https"
	}
	outbound.Header.Set("X-Forwarded-Proto", proto)
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func (e *Engine) transportFor(policy policy.Policy) (*http.Transport, error) {
	key := fmt.Sprintf("%d/%d", policy.UpstreamDialTimeout.Nanoseconds(), policy.UpstreamResponseHeaderTimeout.Nanoseconds())

	e.mu.Lock()
	defer e.mu.Unlock()

	if transport := e.transports[key]; transport != nil {
		return transport, nil
	}

	if policy.UpstreamDialTimeout <= 0 || policy.UpstreamResponseHeaderTimeout <= 0 {
		return nil, errors.New("invalid policy timeouts")
	}

	dialer := &net.Dialer{Timeout: policy.UpstreamDialTimeout}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ResponseHeaderTimeout: policy.UpstreamResponseHeaderTimeout,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		ForceAttemptHTTP2:     true,
	}

	e.transports[key] = transport
	return transport, nil
}

func isRequestTimeout(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.DeadlineExceeded)
}

func isClientCanceled(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.Canceled)
}

func isTimeoutError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

func isDialError(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Op == "dial"
	}
	return false
}

func (e *Engine) passiveFailure(poolKey pool.PoolKey, addr string) {
	if e.registry == nil {
		return
	}
	e.registry.PassiveFailure(poolKey, addr)
}

func (e *Engine) passiveSuccess(poolKey pool.PoolKey, addr string) {
	if e.registry == nil {
		return
	}
	e.registry.PassiveSuccess(poolKey, addr)
}
