package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"modern_reverse_proxy/internal/breaker"
	"modern_reverse_proxy/internal/cache"
	"modern_reverse_proxy/internal/limits"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/outlier"
	"modern_reverse_proxy/internal/plugin"
	"modern_reverse_proxy/internal/policy"
	"modern_reverse_proxy/internal/pool"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/traffic"
)

type Handler struct {
	Store            *runtime.Store
	Registry         *registry.Registry
	RetryRegistry    *registry.RetryRegistry
	BreakerRegistry  *breaker.Registry
	OutlierRegistry  *outlier.Registry
	PluginRegistry   *plugin.Registry
	Engine           *Engine
	Metrics          *obs.Metrics
	Cache            *cache.Cache
	Inflight         *runtime.InflightTracker
	SnapshotObserver SnapshotObserver
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	recorder := NewResponseRecorder(w)
	start := time.Now()
	if h != nil && h.Inflight != nil {
		h.Inflight.Inc()
		defer h.Inflight.Dec()
	}

	requestID := r.Header.Get(RequestIDHeader)
	if requestID == "" {
		requestID = NewRequestID()
	}
	recorder.Header().Set(RequestIDHeader, requestID)
	if strings.HasPrefix(r.URL.Path, "/admin") {
		WriteProxyError(recorder, requestID, http.StatusNotFound, "not_found", "not found")
		return
	}

	ctx := obs.StartTrace(r.Context(), r)
	ctx = context.WithValue(ctx, requestIDKey, requestID)
	r = r.WithContext(ctx)

	logPath := r.URL.Path
	if r.URL.RawQuery != "" {
		logPath = r.URL.Path + "?" + r.URL.RawQuery
	}
	redactQuery := false
	routeID := "none"
	poolKey := "none"
	upstreamAddr := "none"
	snapshotVersion := "none"
	snapshotSource := "none"
	bytesIn := int64(0)
	retryCount := 0
	retryLastReason := ""
	retryBudgetExhausted := false
	cacheStatus := "bypass"
	cacheMetricStatus := ""
	breakerState := ""
	breakerDenied := false
	outlierIgnored := false
	endpointEjected := false
	mtlsRouteRequired := false
	mtlsVerified := false
	trafficVariant := traffic.VariantStable
	cohortMode := "random"
	cohortKeyPresent := false
	overloadRejected := false
	autoDrainActive := false
	trafficPlan := (*traffic.Plan)(nil)
	pluginFilters := []string{}
	pluginTracking := &pluginTracking{}
	tlsEnabled := r.TLS != nil
	canonRoute := ""
	canonObserved := false
	if r.ContentLength > 0 {
		bytesIn = r.ContentLength
	}

	var snap *runtime.Snapshot
	defer func() {
		if snap != nil {
			h.observeSnapshot(SnapshotPhaseResponseWrite, snap)
		}
		if recovered := recover(); recovered != nil {
			recorder.SetErrorCategory("panic")
			if !recorder.WroteHeader() {
				WriteProxyError(recorder, requestID, http.StatusInternalServerError, "panic", "internal server error")
			}
		}

		duration := time.Since(start)
		errorCategory := recorder.ErrorCategory()
		if errorCategory == "" {
			errorCategory = "none"
		}

		variantLabel := string(trafficVariant)
		if variantLabel == "" {
			variantLabel = string(traffic.VariantStable)
		}
		proxyError := errorCategory != "none"
		if trafficPlan != nil && trafficPlan.Stats != nil {
			trafficPlan.Stats.Record(trafficVariant, recorder.Status(), proxyError)
		}

		obs.LogAccess(obs.RequestContext{
			RequestID:            requestID,
			Method:               r.Method,
			Host:                 r.Host,
			Path:                 logPath,
			RouteID:              routeID,
			PoolKey:              poolKey,
			UpstreamAddr:         upstreamAddr,
			PluginFilters:        pluginFilters,
			PluginBypassed:       pluginTracking.bypassed,
			PluginBypassReason:   pluginTracking.bypassReason,
			PluginFailureMode:    pluginTracking.failureMode,
			PluginShortCircuit:   pluginTracking.shortCircuit,
			PluginMutationDenied: pluginTracking.mutationDenied,
			Status:               recorder.Status(),
			Duration:             duration,
			BytesIn:              bytesIn,
			BytesOut:             recorder.BytesWritten(),
			ErrorCategory:        errorCategory,
			RetryCount:           retryCount,
			RetryLastReason:      retryLastReason,
			RetryBudgetExhausted: retryBudgetExhausted,
			CacheStatus:          cacheStatus,
			SnapshotVersion:      snapshotVersion,
			SnapshotSource:       snapshotSource,
			TrafficVariant:       variantLabel,
			CohortMode:           cohortMode,
			CohortKeyPresent:     cohortKeyPresent,
			OverloadRejected:     overloadRejected,
			AutoDrainActive:      autoDrainActive,
			UserAgent:            r.UserAgent(),
			RemoteAddr:           r.RemoteAddr,
			BreakerState:         breakerState,
			BreakerDenied:        breakerDenied,
			OutlierIgnored:       outlierIgnored,
			EndpointEjected:      endpointEjected,
			TLS:                  tlsEnabled,
			MTLSRouteRequired:    mtlsRouteRequired,
			MTLSVerified:         mtlsVerified,
		})

		if h != nil && h.Metrics != nil {
			if !canonObserved {
				canonRoute, _ = h.Metrics.Canonicalize(routeID, poolKey)
			}
			h.Metrics.SetSnapshotInfo(snapshotVersion, snapshotSource)
			h.Metrics.ObserveRequestCanonical(canonRoute, recorder.Status(), duration)
			if errorCategory != "none" {
				h.Metrics.RecordProxyErrorCanonical(canonRoute, errorCategory)
			}
			h.Metrics.RecordVariantRequestCanonical(canonRoute, variantLabel)
			if proxyError || recorder.Status() >= http.StatusInternalServerError {
				h.Metrics.RecordVariantErrorCanonical(canonRoute, variantLabel)
			}
			if overloadRejected {
				h.Metrics.RecordOverloadRejectCanonical(canonRoute)
			}
		}
	}()

	if h == nil || h.Store == nil || h.Engine == nil || h.Registry == nil {
		WriteProxyError(recorder, requestID, http.StatusServiceUnavailable, "bad_gateway", "proxy not ready")
		return
	}

	snap = h.Store.Acquire()
	if snap == nil || snap.Router == nil {
		if snap != nil {
			h.Store.Release(snap)
		}
		WriteProxyError(recorder, requestID, http.StatusServiceUnavailable, "bad_gateway", "snapshot missing")
		return
	}
	defer h.Store.Release(snap)
	snapshotVersion = snap.Version
	snapshotSource = snap.Source
	redactQuery = snap.Logging.RedactQuery
	if redactQuery {
		logPath = r.URL.Path
	}

	if enforceRequestLimits(recorder, requestID, r, snap.Limits) {
		return
	}

	h.observeSnapshot(SnapshotPhaseRouteMatch, snap)
	obs.MarkPhase(r.Context(), "route_match")

	route, ok := snap.Router.Match(r)
	if !ok {
		WriteProxyError(recorder, requestID, http.StatusNotFound, "no_route", "no route matched")
		return
	}
	routeID = route.ID
	if route.Policy.Plugins.Enabled && len(route.Policy.Plugins.Filters) > 0 {
		pluginFilters = make([]string, 0, len(route.Policy.Plugins.Filters))
		for _, filter := range route.Policy.Plugins.Filters {
			pluginFilters = append(pluginFilters, filter.Name)
		}
	}
	mtlsRouteRequired = route.Policy.RequireMTLS
	if route.Policy.RequireMTLS {
		if snap.TLSStore == nil {
			if h.Metrics != nil {
				h.Metrics.RecordMTLSReject(route.ID)
			}
			WriteProxyError(recorder, requestID, http.StatusForbidden, "mtls_required", "client certificate required")
			return
		}
		var rawCerts [][]byte
		if r.TLS != nil {
			rawCerts = make([][]byte, 0, len(r.TLS.PeerCertificates))
			for _, cert := range r.TLS.PeerCertificates {
				if cert == nil {
					continue
				}
				rawCerts = append(rawCerts, cert.Raw)
			}
		}
		if err := snap.TLSStore.VerifyClientCert(rawCerts, nil); err != nil {
			if h.Metrics != nil {
				h.Metrics.RecordMTLSReject(route.ID)
			}
			WriteProxyError(recorder, requestID, http.StatusForbidden, "mtls_required", "client certificate required")
			return
		}
		mtlsVerified = true
	}

	trafficPlan = route.TrafficPlan
	selectedPoolName := route.PoolName
	selectedPoolKey := route.StablePoolKey
	if selectedPoolKey == "" {
		selectedPoolKey = route.ID + "::" + route.PoolName
	}
	if trafficPlan != nil {
		variant, meta := trafficPlan.PickVariant(r)
		trafficVariant = variant
		cohortMode = meta.CohortMode
		cohortKeyPresent = meta.CohortKeyPresent
		autoDrainActive = meta.AutoDrainActive
		if variant == traffic.VariantCanary && route.CanaryPoolName != "" {
			selectedPoolName = route.CanaryPoolName
			selectedPoolKey = route.CanaryPoolKey
			if selectedPoolKey == "" {
				selectedPoolKey = route.ID + "::" + route.CanaryPoolName
			}
		}
	}
	if trafficPlan != nil && trafficPlan.Overload != nil {
		release, ok := trafficPlan.Overload.Acquire(r.Context())
		if !ok {
			overloadRejected = true
			WriteOverload(recorder, requestID)
			return
		}
		defer release()
	}

	poolKeyValue, ok := snap.Pools[selectedPoolName]
	if !ok || poolKeyValue == "" {
		WriteProxyError(recorder, requestID, http.StatusBadGateway, "bad_gateway", "pool not found")
		return
	}
	poolKey = string(poolKeyValue)

	stablePoolKey := selectedPoolKey

	poolConfig, ok := snap.PoolConfigs[selectedPoolName]
	if !ok {
		WriteProxyError(recorder, requestID, http.StatusBadGateway, "bad_gateway", "pool config missing")
		return
	}
	if h.Metrics != nil {
		canonRoute, _ = h.Metrics.Canonicalize(routeID, poolKey)
		canonObserved = true
	}

	ctx, cancel := context.WithTimeout(r.Context(), route.Policy.RequestTimeout)
	defer cancel()

	r = r.WithContext(ctx)
	if h.applyRequestPlugins(recorder, r, route, requestID, pluginTracking) {
		return
	}

	if h.BreakerRegistry != nil {
		state, allowed, err := h.BreakerRegistry.Allow(stablePoolKey, poolConfig.Breaker)
		breakerState = state.String()
		if err != nil {
			breakerState = "error"
		} else if h.Metrics != nil {
			h.Metrics.SetBreakerOpen(stablePoolKey, state == breaker.StateOpen)
		}
		if !allowed {
			breakerDenied = true
			if h.Metrics != nil {
				h.Metrics.RecordCircuitOpen(stablePoolKey)
				h.Metrics.SetBreakerOpen(stablePoolKey, true)
			}
			WriteProxyError(recorder, requestID, http.StatusServiceUnavailable, "circuit_open", "circuit open")
			return
		}
	}

	cachePolicy := route.Policy.Cache
	cacheKey := ""
	cacheEligible := isCacheEligible(r, cachePolicy, h.Cache)
	if cacheEligible {
		cacheKey = cache.BuildKey(r, cachePolicy)
		if h.Cache != nil && h.Cache.Store != nil {
			if entry, ok := h.Cache.Store.Get(cacheKey); ok {
				cacheStatus = "hit"
				cacheMetricStatus = "hit"
				writeCachedResponse(recorder, entry, requestID, r.Method)
				if h.Metrics != nil {
					h.Metrics.RecordCacheRequestCanonical(canonRoute, cacheMetricStatus)
				}
				return
			}
		}
	}
	obs.MarkPhase(r.Context(), "upstream_pick")
	picker := func() (pool.PickResult, bool) {
		h.observeSnapshot(SnapshotPhaseUpstreamPick, snap)
		return h.Registry.Pick(poolKeyValue, func(addr string, now time.Time) bool {
			if h.OutlierRegistry == nil {
				return false
			}
			return h.OutlierRegistry.IsEjected(stablePoolKey, addr, now)
		})
	}
	if cacheEligible {
		cacheStatus = "miss"
		coalesceFlight, isLeader, coalesceApplied := startCoalescing(h.Cache, cacheKey, cachePolicy)
		if coalesceApplied && !isLeader {
			entry, ok, err, completed := h.Cache.Coalescer.Wait(coalesceFlight, cachePolicy.CoalesceTimeout)
			if completed && err == nil && ok {
				cacheStatus = "coalesce_follower"
				cacheMetricStatus = "miss"
				writeCachedResponse(recorder, entry, requestID, r.Method)
				if h.Metrics != nil {
					h.Metrics.RecordCacheRequestCanonical(canonRoute, cacheMetricStatus)
				}
				return
			}
			if !completed {
				cacheStatus = "coalesce_breakaway"
				if h.Metrics != nil {
					h.Metrics.RecordCacheCoalesceBreakawayCanonical(canonRoute)
				}
			}
		}

		var coalesceEntry cache.Entry
		coalesceResult := false
		var coalesceErr error
		if coalesceApplied && isLeader {
			defer func() {
				h.Cache.Coalescer.Finish(cacheKey, coalesceFlight, coalesceEntry, coalesceResult, coalesceErr)
			}()
		}

		retryResult, forwardResult := h.Engine.roundTripWithRetry(r, poolKeyValue, stablePoolKey, picker, route.Policy, route.ID, poolConfig.Breaker)
		if retryResult.Response == nil {
			coalesceErr = retryResult.Err
			if writeProxyErrorForResult(recorder, r, requestID, retryResult) {
				return
			}
			WriteProxyError(recorder, requestID, http.StatusBadGateway, "bad_gateway", "upstream request failed")
			return
		}

		if forwardResult.UpstreamAddr != "" {
			upstreamAddr = forwardResult.UpstreamAddr
		}
		retryCount = forwardResult.RetryCount
		retryLastReason = forwardResult.RetryReason
		retryBudgetExhausted = forwardResult.RetryBudgetExhausted
		outlierIgnored = forwardResult.OutlierIgnored
		endpointEjected = forwardResult.EndpointEjected

		if h.applyResponsePlugins(recorder, r, retryResult.Response, route, requestID, pluginTracking) {
			return
		}

		applyResponseStreamTimeout(retryResult.Response, snap.Limits.ResponseStreamTimeout)

		cacheable, contentLength := isCacheableResponse(retryResult.Response, cachePolicy)
		if !cacheable {
			cacheStatus = "not_cacheable"
			cacheMetricStatus = "not_cacheable"
			coalesceResult = false
			WriteUpstreamResponse(recorder, retryResult.Response, requestID)
			if h.Metrics != nil {
				h.Metrics.RecordCacheRequestCanonical(canonRoute, cacheMetricStatus)
			}
			return
		}

		body, err := readUpstreamBody(retryResult.Response, contentLength)
		if err != nil {
			coalesceErr = err
			WriteProxyError(recorder, requestID, http.StatusBadGateway, "bad_gateway", "upstream request failed")
			return
		}

		entry := cache.Entry{
			Status:    retryResult.Response.StatusCode,
			Header:    cloneHeader(retryResult.Response.Header),
			Body:      body,
			StoredAt:  time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(cachePolicy.TTL),
		}
		coalesceEntry = entry
		coalesceResult = true
		cacheMetricStatus = "miss"
		if h.Cache != nil && h.Cache.Store != nil {
			if err := h.Cache.Store.Set(cacheKey, entry); err != nil {
				if cacheStatus != "coalesce_breakaway" {
					cacheStatus = "store_failed"
				}
				if h.Metrics != nil {
					h.Metrics.RecordCacheStoreFailCanonical(canonRoute)
				}
			}
		}

		writeCachedResponse(recorder, entry, requestID, r.Method)
		if h.Metrics != nil {
			h.Metrics.RecordCacheRequestCanonical(canonRoute, cacheMetricStatus)
		}
		return
	}

	retryResult, forwardResult := h.Engine.roundTripWithRetry(r, poolKeyValue, stablePoolKey, picker, route.Policy, route.ID, poolConfig.Breaker)
	if retryResult.Response == nil {
		if writeProxyErrorForResult(recorder, r, requestID, retryResult) {
			return
		}
		WriteProxyError(recorder, requestID, http.StatusBadGateway, "bad_gateway", "upstream request failed")
		return
	}
	if forwardResult.UpstreamAddr != "" {
		upstreamAddr = forwardResult.UpstreamAddr
	}
	retryCount = forwardResult.RetryCount
	retryLastReason = forwardResult.RetryReason
	retryBudgetExhausted = forwardResult.RetryBudgetExhausted
	outlierIgnored = forwardResult.OutlierIgnored
	endpointEjected = forwardResult.EndpointEjected
	if h.applyResponsePlugins(recorder, r, retryResult.Response, route, requestID, pluginTracking) {
		return
	}
	applyResponseStreamTimeout(retryResult.Response, snap.Limits.ResponseStreamTimeout)
	WriteUpstreamResponse(recorder, retryResult.Response, requestID)
}

func (h *Handler) observeSnapshot(phase string, snapshot *runtime.Snapshot) {
	if h == nil || h.SnapshotObserver == nil || snapshot == nil {
		return
	}
	h.SnapshotObserver.ObserveSnapshot(phase, snapshot)
}

var errBodyTooLarge = errors.New("body too large")

func enforceRequestLimits(recorder *ResponseRecorder, requestID string, request *http.Request, limitConfig limits.Limits) bool {
	if request == nil {
		return false
	}
	if limitConfig.MaxURLBytes > 0 {
		uri := request.URL.RequestURI()
		if len(uri) > limitConfig.MaxURLBytes {
			WriteProxyError(recorder, requestID, http.StatusRequestURITooLong, "uri_too_long", "uri too long")
			return true
		}
	}
	if limitConfig.MaxHeaderCount > 0 && len(request.Header) > limitConfig.MaxHeaderCount {
		WriteProxyError(recorder, requestID, http.StatusRequestHeaderFieldsTooLarge, "headers_too_large", "too many headers")
		return true
	}
	if limitConfig.MaxBodyBytes == 0 {
		if request.Body != nil && request.Body != http.NoBody {
			_ = request.Body.Close()
		}
		if request.ContentLength != 0 {
			WriteProxyError(recorder, requestID, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
			return true
		}
		return false
	}
	if limitConfig.MaxBodyBytes > 0 {
		if request.ContentLength > limitConfig.MaxBodyBytes {
			WriteProxyError(recorder, requestID, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
			return true
		}
		if request.ContentLength < 0 {
			body, err := readBodyWithinLimit(request.Body, limitConfig.MaxBodyBytes)
			if err != nil {
				if errors.Is(err, errBodyTooLarge) {
					WriteProxyError(recorder, requestID, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
					return true
				}
				WriteProxyError(recorder, requestID, http.StatusBadRequest, "request_too_large", "request body unreadable")
				return true
			}
			request.Body = io.NopCloser(bytes.NewReader(body))
			request.ContentLength = int64(len(body))
		}
	}
	return false
}

func readBodyWithinLimit(body io.ReadCloser, limit int64) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	defer body.Close()
	if limit <= 0 {
		return nil, errBodyTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errBodyTooLarge
	}
	return data, nil
}

type deadlineReadCloser struct {
	inner    io.ReadCloser
	deadline time.Time
}

func (d *deadlineReadCloser) Read(buffer []byte) (int, error) {
	if time.Now().After(d.deadline) {
		_ = d.inner.Close()
		return 0, context.DeadlineExceeded
	}
	return d.inner.Read(buffer)
}

func (d *deadlineReadCloser) Close() error {
	return d.inner.Close()
}

func applyResponseStreamTimeout(resp *http.Response, timeout time.Duration) {
	if resp == nil || resp.Body == nil || timeout <= 0 {
		return
	}
	resp.Body = &deadlineReadCloser{inner: resp.Body, deadline: time.Now().Add(timeout)}
}

func isCacheEligible(r *http.Request, cachePolicy policy.CachePolicy, cacheLayer *cache.Cache) bool {
	if cacheLayer == nil || cacheLayer.Store == nil {
		return false
	}
	if !cachePolicy.Enabled || cachePolicy.TTL <= 0 {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return !hasNoStoreHeader(r.Header)
}

func startCoalescing(cacheLayer *cache.Cache, key string, cachePolicy policy.CachePolicy) (*cache.Flight, bool, bool) {
	if cacheLayer == nil || cacheLayer.Coalescer == nil {
		return nil, false, false
	}
	if !cachePolicy.CoalesceEnabled || key == "" {
		return nil, false, false
	}
	return cacheLayer.Coalescer.Start(key)
}

func hasNoStoreHeader(header http.Header) bool {
	values := header.Values("Cache-Control")
	for _, value := range values {
		if hasNoStoreValue(value) {
			return true
		}
	}
	return false
}

func hasNoStoreValue(value string) bool {
	parts := strings.Split(value, ",")
	for _, part := range parts {
		if strings.EqualFold(strings.TrimSpace(part), "no-store") {
			return true
		}
	}
	return false
}

func isCacheableResponse(resp *http.Response, cachePolicy policy.CachePolicy) (bool, int64) {
	if resp == nil {
		return false, 0
	}
	if resp.StatusCode != http.StatusOK {
		return false, 0
	}
	if hasNoStoreHeader(resp.Header) {
		return false, 0
	}
	if hasChunkedEncoding(resp) {
		return false, 0
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
		return false, 0
	}
	contentLengthHeader := strings.TrimSpace(resp.Header.Get("Content-Length"))
	if contentLengthHeader == "" {
		return false, 0
	}
	contentLength, err := strconv.ParseInt(contentLengthHeader, 10, 64)
	if err != nil || contentLength < 0 {
		return false, 0
	}
	if cachePolicy.OnlyIfContentLength && contentLengthHeader == "" {
		return false, 0
	}
	if contentLength > cachePolicy.MaxObjectBytes {
		return false, 0
	}
	return true, contentLength
}

func hasChunkedEncoding(resp *http.Response) bool {
	for _, encoding := range resp.TransferEncoding {
		if strings.EqualFold(encoding, "chunked") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(resp.Header.Get("Transfer-Encoding")), "chunked")
}

func readUpstreamBody(resp *http.Response, contentLength int64) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}
	defer resp.Body.Close()
	limit := contentLength + 1
	if limit <= 0 {
		limit = 1
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > contentLength {
		return nil, io.ErrUnexpectedEOF
	}
	return body, nil
}

func cloneHeader(header http.Header) http.Header {
	result := make(http.Header, len(header))
	for key, values := range header {
		cloned := make([]string, len(values))
		copy(cloned, values)
		result[key] = cloned
	}
	return result
}

func writeCachedResponse(w http.ResponseWriter, entry cache.Entry, requestID string, method string) {
	copyHeaders(w.Header(), entry.Header)
	w.Header().Set(RequestIDHeader, requestID)
	w.WriteHeader(entry.Status)
	if method == http.MethodHead {
		return
	}
	_, _ = w.Write(entry.Body)
}
