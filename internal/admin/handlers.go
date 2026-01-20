package admin

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"modern_reverse_proxy/internal/apply"
	"modern_reverse_proxy/internal/bundle"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/rollout"
	"modern_reverse_proxy/internal/runtime"
)

type handler struct {
	store         *runtime.Store
	apply         *apply.Manager
	auth          *Authenticator
	rateLimiter   *RateLimiter
	adminStore    *Store
	publicKey     ed25519.PublicKey
	allowUnsigned bool
	rollout       *rollout.Manager
	mux           *http.ServeMux
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
	if len(h.publicKey) == ed25519.PublicKeySize && !h.allowUnsigned {
		writeError(w, requestID, http.StatusForbidden, "unsigned apply disabled")
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
		configHash, _ := bundle.HashConfig(body)
		encoded := base64.StdEncoding.EncodeToString(body)
		h.adminStore.Record(bundle.Bundle{
			Meta: bundle.Meta{
				Version:   result.Version,
				CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
				Source:    "admin",
			},
			ConfigBytesB64: encoded,
			ConfigSHA256:   configHash,
		})
	}
	log.Printf("admin_apply request_id=%s version=%s result=success", requestID, result.Version)
	writeJSON(w, requestID, http.StatusOK, map[string]interface{}{"applied": true, "version": result.Version})
}

func (h *handler) handleBundle(w http.ResponseWriter, r *http.Request) {
	requestID := r.Header.Get(proxy.RequestIDHeader)
	if r.Method != http.MethodPost {
		writeError(w, requestID, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if len(h.publicKey) != ed25519.PublicKeySize {
		writeError(w, requestID, http.StatusServiceUnavailable, "public key missing")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, requestID, http.StatusBadRequest, "invalid body")
		return
	}
	var bundlePayload bundle.Bundle
	if err := json.Unmarshal(body, &bundlePayload); err != nil {
		writeError(w, requestID, http.StatusBadRequest, "invalid bundle")
		return
	}
	if err := bundle.VerifyBundle(bundlePayload, h.publicKey); err != nil {
		metrics := obs.DefaultMetrics()
		result := "bad_sig"
		if errors.Is(err, bundle.ErrBadHash) {
			result = "bad_hash"
		}
		if metrics != nil {
			metrics.RecordBundleVerify(result)
		}
		log.Printf("bundle_version=%s verify_result=%s", bundlePayload.Meta.Version, result)
		writeError(w, requestID, http.StatusBadRequest, "invalid signature")
		return
	}
	if metrics := obs.DefaultMetrics(); metrics != nil {
		metrics.RecordBundleVerify("ok")
	}
	log.Printf("bundle_version=%s verify_result=ok", bundlePayload.Meta.Version)

	result, err := h.applyBundle(r, bundlePayload, "")
	if err != nil {
		writeError(w, requestID, http.StatusBadRequest, err.Error())
		return
	}
	if h.adminStore != nil {
		h.adminStore.Record(bundlePayload)
	}
	writeJSON(w, requestID, http.StatusOK, map[string]interface{}{"applied": true, "version": result.Version})
}

func (h *handler) handleRollback(w http.ResponseWriter, r *http.Request) {
	requestID := r.Header.Get(proxy.RequestIDHeader)
	if r.Method != http.MethodPost {
		writeError(w, requestID, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.adminStore == nil {
		writeError(w, requestID, http.StatusServiceUnavailable, "history unavailable")
		return
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, requestID, http.StatusBadRequest, "invalid body")
		return
	}
	rollbackBundle := bundle.Bundle{}
	var ok bool
	if payload.Version != "" {
		rollbackBundle, ok = h.adminStore.Get(payload.Version)
	} else {
		rollbackBundle, ok = h.adminStore.Previous()
	}
	if !ok {
		writeError(w, requestID, http.StatusNotFound, "bundle not found")
		return
	}
	fromVersion := ""
	if h.store != nil {
		if snap := h.store.Get(); snap != nil {
			fromVersion = snap.Version
		}
	}
	result, err := h.applyBundle(r, rollbackBundle, "rollback")
	if err != nil {
		if metrics := obs.DefaultMetrics(); metrics != nil {
			metrics.RecordRollback("error")
		}
		writeError(w, requestID, http.StatusBadRequest, err.Error())
		return
	}
	if metrics := obs.DefaultMetrics(); metrics != nil {
		metrics.RecordRollback("success")
	}
	log.Printf("rollback_from_version=%s rollback_to_version=%s", fromVersion, result.Version)
	if h.adminStore != nil {
		h.adminStore.Record(rollbackBundle)
	}
	writeJSON(w, requestID, http.StatusOK, map[string]interface{}{"rolled_back": true, "version": result.Version})
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

func (h *handler) applyBundle(r *http.Request, bundlePayload bundle.Bundle, sourceOverride string) (*apply.Result, error) {
	if h.rollout != nil {
		return h.rollout.ApplyBundle(r.Context(), bundlePayload, sourceOverride)
	}
	if h.apply == nil {
		return nil, errors.New("apply unavailable")
	}
	configBytes, err := bundlePayload.ConfigBytes()
	if err != nil {
		return nil, err
	}
	source := bundlePayload.Meta.Source
	if sourceOverride != "" {
		source = sourceOverride
	}
	return h.apply.ApplyResolvedVersion(r.Context(), configBytes, source, bundlePayload.Meta.Version, apply.ModeApply)
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
