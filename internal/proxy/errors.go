package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
)

const RequestIDHeader = "X-Request-Id"

type contextKey string

const requestIDKey contextKey = "request_id"

type ProxyErrorBody struct {
	Status        int    `json:"status"`
	RequestID     string `json:"request_id"`
	ErrorCategory string `json:"error_category"`
	Message       string `json:"message"`
}

const overloadRetryAfterSeconds = "1"

func WriteProxyError(w http.ResponseWriter, requestID string, status int, category string, message string) {
	if recorder, ok := w.(errorCategoryWriter); ok {
		recorder.SetErrorCategory(category)
	}
	w.Header().Set(RequestIDHeader, requestID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ProxyErrorBody{
		Status:        status,
		RequestID:     requestID,
		ErrorCategory: category,
		Message:       message,
	})
}

func WriteOverload(w http.ResponseWriter, requestID string) {
	w.Header().Set("Retry-After", overloadRetryAfterSeconds)
	WriteProxyError(w, requestID, http.StatusServiceUnavailable, "overloaded", "overloaded")
}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

func RequestIDFromContext(ctx context.Context) (string, bool) {
	value, ok := ctx.Value(requestIDKey).(string)
	return value, ok
}

func NewRequestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}
