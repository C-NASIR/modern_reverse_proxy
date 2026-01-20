package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"modern_reverse_proxy/internal/breaker"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/outlier"
	"modern_reverse_proxy/internal/policy"
	"modern_reverse_proxy/internal/pool"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/retry"
)

type Engine struct {
	registry   *registry.Registry
	retryReg   *registry.RetryRegistry
	metrics    *obs.Metrics
	breakerReg *breaker.Registry
	outlierReg *outlier.Registry
}

func NewEngine(reg *registry.Registry, retryReg *registry.RetryRegistry, metrics *obs.Metrics, breakerReg *breaker.Registry, outlierReg *outlier.Registry) *Engine {
	return &Engine{registry: reg, retryReg: retryReg, metrics: metrics, breakerReg: breakerReg, outlierReg: outlierReg}
}

type ForwardResult struct {
	RetryCount           int
	RetryReason          string
	RetryBudgetExhausted bool
	UpstreamAddr         string
	SelectedHealthy      bool
	SelectedFailOpen     bool
	OutlierIgnored       bool
	EndpointEjected      bool
}

var errNoUpstream = errors.New("no upstream available")
var errTransportUnavailable = errors.New("upstream transport unavailable")

func (e *Engine) ForwardWithRetry(w http.ResponseWriter, r *http.Request, poolKey pool.PoolKey, stablePoolKey string, picker func() (pool.PickResult, bool), policy policy.Policy, routeID string, breakerCfg breaker.Config, requestID string) ForwardResult {
	retryResult, result := e.roundTripWithRetry(r, poolKey, stablePoolKey, picker, policy, routeID, breakerCfg)
	if retryResult.Response != nil {
		WriteUpstreamResponse(w, retryResult.Response, requestID)
		return result
	}
	if writeProxyErrorForResult(w, r, requestID, retryResult) {
		return result
	}
	WriteProxyError(w, requestID, http.StatusBadGateway, "bad_gateway", "upstream request failed")
	return result
}

func (e *Engine) roundTripWithRetry(r *http.Request, poolKey pool.PoolKey, stablePoolKey string, picker func() (pool.PickResult, bool), policy policy.Policy, routeID string, breakerCfg breaker.Config) (retry.Result, ForwardResult) {
	result := ForwardResult{}
	if picker == nil {
		return retry.Result{Err: errNoUpstream}, result
	}

	transport, err := e.transportFor(poolKey)
	if err != nil {
		return retry.Result{Err: errTransportUnavailable}, result
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

	var lastPick pool.PickResult
	attempt := func(ctx context.Context) (*http.Response, error, string) {
		pickResult, ok := picker()
		if !ok || pickResult.Addr == "" {
			lastPick = pool.PickResult{}
			return nil, errNoUpstream, ""
		}
		lastPick = pickResult
		if pickResult.OutlierIgnored && e.metrics != nil {
			e.metrics.RecordOutlierFailOpen(stablePoolKey)
		}

		upstreamAddr := pickResult.Addr

		if e.registry != nil {
			e.registry.InflightStart(poolKey, upstreamAddr)
			defer e.registry.InflightDone(poolKey, upstreamAddr)
		}

		roundtripStart := time.Now()
		resp, err := roundTripUpstream(ctx, r, upstreamAddr, transport, body)
		if upstreamAddr != "" {
			requestLatency := time.Since(roundtripStart)
			success := err == nil && resp != nil && resp.StatusCode < http.StatusInternalServerError
			reportable := err == nil || (!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded))
			if reportable {
				if e.outlierReg != nil {
					e.outlierReg.RecordResult(stablePoolKey, upstreamAddr, success, requestLatency)
				}
				if e.breakerReg != nil && !errors.Is(err, errNoUpstream) {
					e.breakerReg.Report(stablePoolKey, breakerCfg, success)
				}
			}
		}
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
	result.SelectedHealthy = lastPick.SelectedHealthy
	result.SelectedFailOpen = lastPick.SelectedFailOpen
	result.OutlierIgnored = lastPick.OutlierIgnored
	result.EndpointEjected = lastPick.EndpointEjected

	if retryResult.RetryBudgetExhausted && e.metrics != nil {
		e.metrics.RecordRetryBudgetExhausted(routeID)
	}

	if retryResult.Response != nil {
		e.passiveSuccess(poolKey, retryResult.UpstreamAddr)
		if retryResult.Response.StatusCode >= http.StatusOK && retryResult.Response.StatusCode < http.StatusBadRequest {
			if routeBudget != nil {
				routeBudget.RecordSuccess()
			}
			if clientBudget != nil {
				clientBudget.RecordSuccess()
			}
		}
	}

	return retryResult, result
}

func writeProxyErrorForResult(w http.ResponseWriter, r *http.Request, requestID string, retryResult retry.Result) bool {
	if errors.Is(retryResult.Err, errTransportUnavailable) {
		WriteProxyError(w, requestID, http.StatusBadGateway, "bad_gateway", "upstream transport unavailable")
		return true
	}
	if errors.Is(retryResult.Err, errNoUpstream) {
		WriteProxyError(w, requestID, http.StatusBadGateway, "bad_gateway", "no upstream available")
		return true
	}
	if isRequestTimeout(r.Context()) {
		WriteProxyError(w, requestID, http.StatusGatewayTimeout, "request_timeout", "request timed out")
		return true
	}
	if isClientCanceled(r.Context()) {
		return true
	}
	if retryResult.Err != nil && (isTimeoutError(retryResult.Err) || errors.Is(retryResult.Err, context.DeadlineExceeded)) {
		WriteProxyError(w, requestID, http.StatusGatewayTimeout, "upstream_timeout", "upstream timeout")
		return true
	}
	if retryResult.Err != nil && isDialError(retryResult.Err) {
		WriteProxyError(w, requestID, http.StatusBadGateway, "upstream_connect_failed", "upstream connect failed")
		return true
	}
	return false
}

func (e *Engine) RoundTripUpstream(ctx context.Context, req *http.Request, upstreamAddr string, poolKey pool.PoolKey) (*http.Response, error) {
	transport, err := e.transportFor(poolKey)
	if err != nil {
		return nil, err
	}
	body := req.Body
	if req.Body != nil && req.ContentLength == 0 {
		body = http.NoBody
	}
	return roundTripUpstream(ctx, req, upstreamAddr, transport, body)
}

func roundTripUpstream(ctx context.Context, req *http.Request, upstreamAddr string, transport *http.Transport, body io.ReadCloser) (*http.Response, error) {
	target := &url.URL{
		Scheme:   "http",
		Host:     upstreamAddr,
		Path:     req.URL.Path,
		RawQuery: req.URL.RawQuery,
	}

	if ctx == nil {
		ctx = context.Background()
	}

	outbound, err := http.NewRequestWithContext(ctx, req.Method, target.String(), body)
	if err != nil {
		return nil, err
	}

	outbound.Header = req.Header.Clone()
	outbound.Host = upstreamAddr
	setForwardedHeaders(outbound, req)
	obs.InjectTraceHeaders(outbound, req.Context())

	obs.MarkPhase(req.Context(), "upstream_roundtrip_start")
	resp, err := transport.RoundTrip(outbound)
	obs.MarkPhase(req.Context(), "upstream_roundtrip_end")
	return resp, err
}

func WriteUpstreamResponse(w http.ResponseWriter, resp *http.Response, requestID string) {
	if resp == nil {
		return
	}
	defer resp.Body.Close()
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

func (e *Engine) transportFor(poolKey pool.PoolKey) (*http.Transport, error) {
	if e == nil || e.registry == nil {
		return nil, errors.New("registry unavailable")
	}
	transport := e.registry.Transport(poolKey)
	if transport == nil {
		return nil, errTransportUnavailable
	}
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

func (e *Engine) CloseIdleConnections() {
	if e == nil {
		return
	}
	if e.registry != nil {
		e.registry.CloseIdleConnections()
	}
}
