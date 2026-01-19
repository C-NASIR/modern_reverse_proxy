package proxy

import (
	"context"
	"net/http"
	"time"

	"modern_reverse_proxy/internal/breaker"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/outlier"
	"modern_reverse_proxy/internal/pool"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
)

type Handler struct {
	Store           *runtime.Store
	Registry        *registry.Registry
	RetryRegistry   *registry.RetryRegistry
	BreakerRegistry *breaker.Registry
	OutlierRegistry *outlier.Registry
	Engine          *Engine
	Metrics         *obs.Metrics
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	recorder := NewResponseRecorder(w)
	start := time.Now()

	requestID := r.Header.Get(RequestIDHeader)
	if requestID == "" {
		requestID = NewRequestID()
		if requestID == "" {
			requestID = time.Now().UTC().Format("20060102150405.000000000")
		}
	}
	recorder.Header().Set(RequestIDHeader, requestID)

	ctx := obs.StartTrace(r.Context(), r)
	ctx = WithRequestID(ctx, requestID)
	r = r.WithContext(ctx)

	routeID := "none"
	poolKey := "none"
	upstreamAddr := "none"
	snapshotVersion := "none"
	snapshotSource := "none"
	bytesIn := int64(0)
	retryCount := 0
	retryLastReason := ""
	retryBudgetExhausted := false
	breakerState := ""
	breakerDenied := false
	outlierIgnored := false
	endpointEjected := false
	mtlsRouteRequired := false
	mtlsVerified := false
	tlsEnabled := r.TLS != nil
	if r.ContentLength > 0 {
		bytesIn = r.ContentLength
	}

	defer func() {
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

		obs.LogAccess(obs.RequestContext{
			RequestID:            requestID,
			Method:               r.Method,
			Host:                 r.Host,
			Path:                 r.URL.Path,
			RouteID:              routeID,
			PoolKey:              poolKey,
			UpstreamAddr:         upstreamAddr,
			Status:               recorder.Status(),
			Duration:             duration,
			BytesIn:              bytesIn,
			BytesOut:             recorder.BytesWritten(),
			ErrorCategory:        errorCategory,
			RetryCount:           retryCount,
			RetryLastReason:      retryLastReason,
			RetryBudgetExhausted: retryBudgetExhausted,
			SnapshotVersion:      snapshotVersion,
			SnapshotSource:       snapshotSource,
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
			h.Metrics.SetSnapshotInfo(snapshotVersion, snapshotSource)
			h.Metrics.ObserveRequest(routeID, poolKey, recorder.Status(), duration)
			if errorCategory != "none" {
				h.Metrics.RecordProxyError(routeID, errorCategory)
			}
		}
	}()

	if h == nil || h.Store == nil || h.Engine == nil || h.Registry == nil {
		WriteProxyError(recorder, requestID, http.StatusServiceUnavailable, "bad_gateway", "proxy not ready")
		return
	}

	snap := h.Store.Get()
	if snap == nil || snap.Router == nil {
		WriteProxyError(recorder, requestID, http.StatusServiceUnavailable, "bad_gateway", "snapshot missing")
		return
	}
	snapshotVersion = snap.Version
	snapshotSource = snap.Source

	obs.MarkPhase(r.Context(), "route_match")

	route, ok := snap.Router.Match(r)
	if !ok {
		WriteProxyError(recorder, requestID, http.StatusNotFound, "no_route", "no route matched")
		return
	}
	routeID = route.ID
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

	poolKeyValue, ok := snap.Pools[route.PoolName]
	if !ok || poolKeyValue == "" {
		WriteProxyError(recorder, requestID, http.StatusBadGateway, "bad_gateway", "pool not found")
		return
	}
	poolKey = string(poolKeyValue)

	stablePoolKey := route.StablePoolKey
	if stablePoolKey == "" {
		stablePoolKey = route.ID + "::" + route.PoolName
	}

	poolConfig, ok := snap.PoolConfigs[route.PoolName]
	if !ok {
		WriteProxyError(recorder, requestID, http.StatusBadGateway, "bad_gateway", "pool config missing")
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

	ctx, cancel := context.WithTimeout(r.Context(), route.Policy.RequestTimeout)
	defer cancel()

	r = r.WithContext(ctx)
	obs.MarkPhase(r.Context(), "upstream_pick")
	picker := func() (pool.PickResult, bool) {
		return h.Registry.Pick(poolKeyValue, func(addr string, now time.Time) bool {
			if h.OutlierRegistry == nil {
				return false
			}
			return h.OutlierRegistry.IsEjected(stablePoolKey, addr, now)
		})
	}
	forwardResult := h.Engine.ForwardWithRetry(recorder, r, poolKeyValue, stablePoolKey, picker, route.Policy, route.ID, poolConfig.Breaker, requestID)
	if forwardResult.UpstreamAddr != "" {
		upstreamAddr = forwardResult.UpstreamAddr
	}
	retryCount = forwardResult.RetryCount
	retryLastReason = forwardResult.RetryReason
	retryBudgetExhausted = forwardResult.RetryBudgetExhausted
	outlierIgnored = forwardResult.OutlierIgnored
	endpointEjected = forwardResult.EndpointEjected
}
