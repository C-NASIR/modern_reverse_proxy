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

	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/policy"
	"modern_reverse_proxy/internal/pool"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/retry"
)

type Engine struct {
	mu         sync.Mutex
	transports map[string]*http.Transport
	registry   *registry.Registry
	retryReg   *registry.RetryRegistry
	metrics    *obs.Metrics
}

func NewEngine(reg *registry.Registry, retryReg *registry.RetryRegistry, metrics *obs.Metrics) *Engine {
	return &Engine{transports: make(map[string]*http.Transport), registry: reg, retryReg: retryReg, metrics: metrics}
}

type ForwardResult struct {
	RetryCount           int
	RetryReason          string
	RetryBudgetExhausted bool
	UpstreamAddr         string
}

var errNoUpstream = errors.New("no upstream available")

func (e *Engine) ForwardWithRetry(w http.ResponseWriter, r *http.Request, poolKey pool.PoolKey, picker func() (string, bool), policy policy.Policy, routeID string, requestID string) ForwardResult {
	result := ForwardResult{}
	if picker == nil {
		WriteProxyError(w, requestID, http.StatusBadGateway, "bad_gateway", "no upstream available")
		return result
	}

	transport, err := e.transportFor(policy)
	if err != nil {
		WriteProxyError(w, requestID, http.StatusBadGateway, "bad_gateway", "upstream transport unavailable")
		return result
	}

	allowRetry := policy.Retry.Enabled && policy.Retry.MaxAttempts > 1
	allowRetry = allowRetry && retry.IsIdempotentMethod(r.Method)
	allowRetry = allowRetry && retry.IsReplayableBody(r)

	budgetEnabled := policy.RetryBudget.Enabled || policy.ClientRetryCap.Enabled
	budgetErr := false
	var routeBudget *retry.Budget
	var clientBudget *retry.Budget
	if budgetEnabled {
		if e.retryReg == nil {
			budgetErr = true
		} else {
			var clientCap *retry.ClientCap
			routeBudget, clientCap, err = e.retryReg.Budgets(routeID, policy.RetryBudget, policy.ClientRetryCap)
			if err != nil {
				budgetErr = true
			} else if clientCap != nil {
				clientKey := retry.ClientKey(r, policy.ClientRetryCap)
				clientBudget = clientCap.Bucket(clientKey)
			}
		}
	}

	body := r.Body
	if r.Body != nil && r.ContentLength == 0 {
		body = http.NoBody
	}

	attempt := func(ctx context.Context) (*http.Response, error, string) {
		upstreamAddr, _ := picker()
		if upstreamAddr == "" {
			return nil, errNoUpstream, ""
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

		outbound, err := http.NewRequestWithContext(ctx, r.Method, target.String(), body)
		if err != nil {
			return nil, err, upstreamAddr
		}

		outbound.Header = r.Header.Clone()
		outbound.Host = upstreamAddr
		setForwardedHeaders(outbound, r)
		obs.InjectTraceHeaders(outbound, r.Context())

		obs.MarkPhase(r.Context(), "upstream_roundtrip_start")
		roundtripStart := time.Now()
		resp, err := transport.RoundTrip(outbound)
		obs.MarkPhase(r.Context(), "upstream_roundtrip_end")
		if e.metrics != nil {
			e.metrics.ObserveUpstreamRoundTrip(string(poolKey), time.Since(roundtripStart))
		}
		if err != nil {
			if errors.Is(err, errNoUpstream) {
				return nil, err, upstreamAddr
			}
			if isTimeoutError(err) || errors.Is(err, context.DeadlineExceeded) {
				e.recordUpstreamError(poolKey, "timeout")
				e.passiveFailure(poolKey, upstreamAddr)
				return nil, err, upstreamAddr
			}
			if isDialError(err) {
				e.recordUpstreamError(poolKey, "connect_failed")
				e.passiveFailure(poolKey, upstreamAddr)
				return nil, err, upstreamAddr
			}
			e.recordUpstreamError(poolKey, "other")
			return nil, err, upstreamAddr
		}
		return resp, nil, upstreamAddr
	}

	retryResult := retry.Execute(retry.Config{
		Policy:        policy.Retry,
		OuterContext:  r.Context(),
		AllowRetry:    allowRetry,
		BudgetEnabled: budgetEnabled,
		BudgetError:   budgetErr,
		Budgets: retry.Budgets{
			Route:  routeBudget,
			Client: clientBudget,
		},
		OnRetry: func(reason string) {
			if e.metrics != nil {
				e.metrics.RecordRetry(routeID, reason)
			}
		},
	}, attempt)

	result.RetryCount = retryResult.RetryCount
	result.RetryReason = retryResult.RetryReason
	result.RetryBudgetExhausted = retryResult.RetryBudgetExhausted
	result.UpstreamAddr = retryResult.UpstreamAddr

	if retryResult.RetryBudgetExhausted && e.metrics != nil {
		e.metrics.RecordRetryBudgetExhausted(routeID)
	}

	if retryResult.Response != nil {
		defer retryResult.Response.Body.Close()
		e.passiveSuccess(poolKey, retryResult.UpstreamAddr)
		if retryResult.Response.StatusCode >= http.StatusOK && retryResult.Response.StatusCode < http.StatusBadRequest {
			if routeBudget != nil {
				routeBudget.RecordSuccess()
			}
			if clientBudget != nil {
				clientBudget.RecordSuccess()
			}
		}

		copyHeaders(w.Header(), retryResult.Response.Header)
		w.Header().Set(RequestIDHeader, requestID)
		w.WriteHeader(retryResult.Response.StatusCode)
		_, _ = io.Copy(w, retryResult.Response.Body)
		return result
	}

	if errors.Is(retryResult.Err, errNoUpstream) {
		WriteProxyError(w, requestID, http.StatusBadGateway, "bad_gateway", "no upstream available")
		return result
	}
	if isRequestTimeout(r.Context()) {
		WriteProxyError(w, requestID, http.StatusGatewayTimeout, "request_timeout", "request timed out")
		return result
	}
	if isClientCanceled(r.Context()) {
		return result
	}
	if retryResult.Err != nil && (isTimeoutError(retryResult.Err) || errors.Is(retryResult.Err, context.DeadlineExceeded)) {
		WriteProxyError(w, requestID, http.StatusGatewayTimeout, "upstream_timeout", "upstream timeout")
		return result
	}
	if retryResult.Err != nil && isDialError(retryResult.Err) {
		WriteProxyError(w, requestID, http.StatusBadGateway, "upstream_connect_failed", "upstream connect failed")
		return result
	}
	WriteProxyError(w, requestID, http.StatusBadGateway, "bad_gateway", "upstream request failed")
	return result
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

func (e *Engine) recordUpstreamError(poolKey pool.PoolKey, category string) {
	if e.metrics == nil {
		return
	}
	e.metrics.RecordUpstreamError(string(poolKey), category)
}
