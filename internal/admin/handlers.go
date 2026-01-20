package admin

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"modern_reverse_proxy/internal/apply"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/runtime"
)

type handler struct {
	store       *runtime.Store
	apply       *apply.Manager
	auth        *Authenticator
	rateLimiter *RateLimiter
	adminStore  *Store
	mux         *http.ServeMux
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := r.Header.Get(proxy.RequestIDHeader)
	if requestID == "" {
		requestID = proxy.NewRequestID()
		if requestID == "" {
			requestID = time.Now().UTC().Format("20060102150405.000000000")
		}
	}
	w.Header().Set(proxy.RequestIDHeader, requestID)

	if h.rateLimiter != nil {
		if !h.rateLimiter.Allow(r.RemoteAddr) {
			writeError(w, requestID, http.StatusTooManyRequests, "rate_limited")
			return
		}
	}

	if h.auth == nil {
		writeError(w, requestID, http.StatusUnauthorized, "auth unavailable")
		return
	}
	if err := h.auth.Authenticate(r); err != nil {
		if h.rateLimiter != nil {
			h.rateLimiter.RecordFailure(r.RemoteAddr)
		}
		status := http.StatusUnauthorized
		message := "unauthorized"
		var authErr *AuthError
		if errors.As(err, &authErr) {
			status = authErr.Status
			message = authErr.Message
		}
		writeError(w, requestID, status, message)
		return
	}
	if h.rateLimiter != nil {
		h.rateLimiter.ResetFailures(r.RemoteAddr)
	}

	h.mux.ServeHTTP(w, r)
}

func (h *handler) handleValidate(w http.ResponseWriter, r *http.Request) {
	requestID := r.Header.Get(proxy.RequestIDHeader)
	if r.Method != http.MethodPost {
		writeError(w, requestID, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, requestID, http.StatusBadRequest, "invalid body")
		return
	}
	if h.apply == nil {
		writeError(w, requestID, http.StatusServiceUnavailable, "apply unavailable")
		return
	}
	result, err := h.apply.Apply(r.Context(), body, "admin", apply.ModeValidate)
	if err != nil {
		status, message := applyErrorStatus(err)
		writeError(w, requestID, status, message)
		return
	}
	_ = result
	writeJSON(w, requestID, http.StatusOK, map[string]bool{"ok": true})
}

func (h *handler) handleApply(w http.ResponseWriter, r *http.Request) {
	requestID := r.Header.Get(proxy.RequestIDHeader)
	if r.Method != http.MethodPost {
		writeError(w, requestID, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, requestID, http.StatusBadRequest, "invalid body")
		return
	}
	if h.apply == nil {
		writeError(w, requestID, http.StatusServiceUnavailable, "apply unavailable")
		return
	}

	version := apply.ConfigVersion(body)
	result, err := h.apply.Apply(r.Context(), body, "admin", apply.ModeApply)
	if err != nil {
		status, message := applyErrorStatus(err)
		log.Printf("admin_apply request_id=%s version=%s result=error reason=%s", requestID, version, message)
		writeError(w, requestID, status, message)
		return
	}
	if h.adminStore != nil {
		h.adminStore.Set(result.Version, body, result.Config)
	}
	log.Printf("admin_apply request_id=%s version=%s result=success", requestID, result.Version)
	writeJSON(w, requestID, http.StatusOK, map[string]interface{}{"applied": true, "version": result.Version})
}

func (h *handler) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	requestID := r.Header.Get(proxy.RequestIDHeader)
	if r.Method != http.MethodGet {
		writeError(w, requestID, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.store == nil {
		writeError(w, requestID, http.StatusServiceUnavailable, "snapshot unavailable")
		return
	}
	snap := h.store.Get()
	if snap == nil {
		writeError(w, requestID, http.StatusNotFound, "snapshot missing")
		return
	}
	poolCount := 0
	if snap.Pools != nil {
		poolCount = len(snap.Pools)
	}
	writeJSON(w, requestID, http.StatusOK, map[string]interface{}{
		"version":     snap.Version,
		"created_at":  snap.CreatedAt,
		"source":      snap.Source,
		"route_count": snap.RouteCount,
		"pool_count":  poolCount,
	})
}

func applyErrorStatus(err error) (int, string) {
	if err == nil {
		return http.StatusOK, ""
	}
	switch {
	case errors.Is(err, apply.ErrConfigTooLarge):
		return http.StatusRequestEntityTooLarge, err.Error()
	case errors.Is(err, apply.ErrCompileTimeout):
		return http.StatusTooManyRequests, err.Error()
	case errors.Is(err, apply.ErrPressure):
		return http.StatusTooManyRequests, err.Error()
	default:
		return http.StatusBadRequest, err.Error()
	}
}

func writeError(w http.ResponseWriter, requestID string, status int, message string) {
	writeJSON(w, requestID, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, requestID string, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(proxy.RequestIDHeader, requestID)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
