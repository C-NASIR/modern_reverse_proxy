package proxy

import (
	"context"
	"net/http"
	"time"

	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
)

type Handler struct {
	Store         *runtime.Store
	Registry      *registry.Registry
	RetryRegistry *registry.RetryRegistry
	Engine        *Engine
	Metrics       *obs.Metrics
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

	poolKeyValue, ok := snap.Pools[route.PoolName]
	if !ok || poolKeyValue == "" {
		WriteProxyError(recorder, requestID, http.StatusBadGateway, "bad_gateway", "pool not found")
		return
	}
	poolKey = string(poolKeyValue)

	ctx, cancel := context.WithTimeout(r.Context(), route.Policy.RequestTimeout)
	defer cancel()

	r = r.WithContext(ctx)
	obs.MarkPhase(r.Context(), "upstream_pick")
	picker := func() (string, bool) {
		return h.Registry.Pick(poolKeyValue)
	}
	forwardResult := h.Engine.ForwardWithRetry(recorder, r, poolKeyValue, picker, route.Policy, route.ID, requestID)
	if forwardResult.UpstreamAddr != "" {
		upstreamAddr = forwardResult.UpstreamAddr
	}
	retryCount = forwardResult.RetryCount
	retryLastReason = forwardResult.RetryReason
	retryBudgetExhausted = forwardResult.RetryBudgetExhausted
}
