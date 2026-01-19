package proxy

import (
	"net/http"

	"modern_reverse_proxy/internal/runtime"
)

type Handler struct {
	Store  *runtime.Store
	Engine *Engine
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Store == nil || h.Engine == nil {
		http.Error(w, "proxy not ready", http.StatusServiceUnavailable)
		return
	}

	snap := h.Store.Get()
	if snap == nil || snap.Router == nil {
		http.Error(w, "snapshot missing", http.StatusServiceUnavailable)
		return
	}

	route, ok := snap.Router.Match(r)
	if !ok {
		http.NotFound(w, r)
		return
	}

	pool := snap.Pools[route.PoolName]
	if pool == nil {
		http.Error(w, "pool not found", http.StatusBadGateway)
		return
	}

	upstream := pool.Pick()
	if upstream == "" {
		http.Error(w, "no upstream available", http.StatusBadGateway)
		return
	}

	h.Engine.Forward(w, r, upstream)
}
