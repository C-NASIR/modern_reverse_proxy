package proxy

import (
	"context"
	"net/http"
	"time"

	"modern_reverse_proxy/internal/runtime"
)

type Handler struct {
	Store  *runtime.Store
	Engine *Engine
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Store == nil || h.Engine == nil {
		WriteProxyError(w, "", http.StatusServiceUnavailable, "bad_gateway", "proxy not ready")
		return
	}

	requestID := r.Header.Get(RequestIDHeader)
	if requestID == "" {
		requestID = NewRequestID()
		if requestID == "" {
			requestID = time.Now().UTC().Format("20060102150405.000000000")
		}
	}
	w.Header().Set(RequestIDHeader, requestID)

	ctx := WithRequestID(r.Context(), requestID)
	r = r.WithContext(ctx)

	snap := h.Store.Get()
	if snap == nil || snap.Router == nil {
		WriteProxyError(w, requestID, http.StatusServiceUnavailable, "bad_gateway", "snapshot missing")
		return
	}

	route, ok := snap.Router.Match(r)
	if !ok {
		WriteProxyError(w, requestID, http.StatusNotFound, "no_route", "no route matched")
		return
	}

	pool := snap.Pools[route.PoolName]
	if pool == nil {
		WriteProxyError(w, requestID, http.StatusBadGateway, "bad_gateway", "pool not found")
		return
	}

	upstream := pool.Pick()
	if upstream == "" {
		WriteProxyError(w, requestID, http.StatusBadGateway, "bad_gateway", "no upstream available")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), route.Policy.RequestTimeout)
	defer cancel()

	r = r.WithContext(ctx)
	h.Engine.Forward(w, r, upstream, route.Policy, requestID)
}
