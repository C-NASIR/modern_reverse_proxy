package obs

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type AccessLogEntry struct {
	Timestamp       string `json:"ts"`
	RequestID       string `json:"request_id"`
	Method          string `json:"method"`
	Host            string `json:"host"`
	Path            string `json:"path"`
	RouteID         string `json:"route_id"`
	PoolKey         string `json:"pool_key"`
	UpstreamAddr    string `json:"upstream_addr"`
	Status          int    `json:"status"`
	DurationMS      int64  `json:"duration_ms"`
	BytesIn         int64  `json:"bytes_in"`
	BytesOut        int64  `json:"bytes_out"`
	ErrorCategory   string `json:"error_category"`
	SnapshotVersion string `json:"snapshot_version"`
	UserAgent       string `json:"user_agent,omitempty"`
	RemoteAddr      string `json:"remote_addr,omitempty"`
}

func LogAccess(ctx RequestContext) {
	entry := AccessLogEntry{
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		RequestID:       defaultString(ctx.RequestID, "none"),
		Method:          ctx.Method,
		Host:            ctx.Host,
		Path:            ctx.Path,
		RouteID:         defaultString(ctx.RouteID, "none"),
		PoolKey:         defaultString(ctx.PoolKey, "none"),
		UpstreamAddr:    defaultString(ctx.UpstreamAddr, "none"),
		Status:          ctx.Status,
		DurationMS:      ctx.Duration.Milliseconds(),
		BytesIn:         ctx.BytesIn,
		BytesOut:        ctx.BytesOut,
		ErrorCategory:   defaultString(ctx.ErrorCategory, "none"),
		SnapshotVersion: defaultString(ctx.SnapshotVersion, "none"),
		UserAgent:       ctx.UserAgent,
		RemoteAddr:      ctx.RemoteAddr,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stdout, "log_marshal_error request_id=%s error=%v\n", entry.RequestID, err)
		return
	}
	_, _ = os.Stdout.Write(append(data, '\n'))
}

func defaultString(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
