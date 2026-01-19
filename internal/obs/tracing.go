package obs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

type TraceContext struct {
	TraceParent string
	TraceState  string
	SpanID      string
	mu          sync.Mutex
	phases      map[string]time.Time
}

type traceKey struct{}

func StartTrace(ctx context.Context, req *http.Request) context.Context {
	trace := &TraceContext{
		TraceParent: req.Header.Get("traceparent"),
		TraceState:  req.Header.Get("tracestate"),
		SpanID:      newSpanID(),
		phases:      make(map[string]time.Time),
	}
	return context.WithValue(ctx, traceKey{}, trace)
}

func TraceFromContext(ctx context.Context) (*TraceContext, bool) {
	trace, ok := ctx.Value(traceKey{}).(*TraceContext)
	return trace, ok
}

func InjectTraceHeaders(req *http.Request, ctx context.Context) {
	trace, ok := TraceFromContext(ctx)
	if !ok {
		return
	}
	if trace.TraceParent != "" {
		req.Header.Set("traceparent", trace.TraceParent)
	}
	if trace.TraceState != "" {
		req.Header.Set("tracestate", trace.TraceState)
	}
}

func MarkPhase(ctx context.Context, name string) {
	trace, ok := TraceFromContext(ctx)
	if !ok {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.phases[name] = time.Now()
}

func newSpanID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}
