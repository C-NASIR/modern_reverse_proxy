package proxy

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

type PoolRuntime struct {
	endpoints []string
	rr        uint64
}

func NewPoolRuntime(endpoints []string) *PoolRuntime {
	return &PoolRuntime{endpoints: append([]string(nil), endpoints...)}
}

func (p *PoolRuntime) Pick() string {
	if p == nil || len(p.endpoints) == 0 {
		return ""
	}
	idx := atomic.AddUint64(&p.rr, 1) - 1
	return p.endpoints[idx%uint64(len(p.endpoints))]
}

type Engine struct {
	Transport *http.Transport
}

func NewEngine() *Engine {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     30 * time.Second,
	}
	return &Engine{Transport: transport}
}

func (e *Engine) Forward(w http.ResponseWriter, r *http.Request, upstreamAddr string) {
	if upstreamAddr == "" {
		http.Error(w, "no upstream available", http.StatusBadGateway)
		return
	}

	target := &url.URL{
		Scheme:   "http",
		Host:     upstreamAddr,
		Path:     r.URL.Path,
		RawQuery: r.URL.RawQuery,
	}

	outbound, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		http.Error(w, "failed to create upstream request", http.StatusBadGateway)
		return
	}

	outbound.Header = r.Header.Clone()
	outbound.Host = upstreamAddr
	setForwardedHeaders(outbound, r)

	resp, err := e.Transport.RoundTrip(outbound)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
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
