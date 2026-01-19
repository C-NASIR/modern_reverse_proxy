package obs

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type AccessLogEntry struct {
	Timestamp            string `json:"ts"`
	RequestID            string `json:"request_id"`
	Method               string `json:"method"`
	Host                 string `json:"host"`
	Path                 string `json:"path"`
	RouteID              string `json:"route_id"`
	PoolKey              string `json:"pool_key"`
	UpstreamAddr         string `json:"upstream_addr"`
	Status               int    `json:"status"`
	DurationMS           int64  `json:"duration_ms"`
	BytesIn              int64  `json:"bytes_in"`
	BytesOut             int64  `json:"bytes_out"`
	ErrorCategory        string `json:"error_category"`
	RetryCount           int    `json:"retry_count"`
	RetryLastReason      string `json:"retry_last_reason"`
	RetryBudgetExhausted bool   `json:"retry_budget_exhausted"`
	CacheStatus          string `json:"cache_status"`
	SnapshotVersion      string `json:"snapshot_version"`
	TrafficVariant       string `json:"traffic_variant"`
	CohortMode           string `json:"cohort_mode"`
	CohortKeyPresent     bool   `json:"cohort_key_present"`
	OverloadRejected     bool   `json:"overload_rejected"`
	AutoDrainActive      bool   `json:"autodrain_active"`
	UserAgent            string `json:"user_agent,omitempty"`
	RemoteAddr           string `json:"remote_addr,omitempty"`
	BreakerState         string `json:"breaker_state,omitempty"`
	BreakerDenied        bool   `json:"breaker_denied"`
	OutlierIgnored       bool   `json:"outlier_ignored"`
	EndpointEjected      bool   `json:"endpoint_ejected"`
	TLS                  bool   `json:"tls"`
	MTLSRouteRequired    bool   `json:"mtls_route_required"`
	MTLSVerified         bool   `json:"mtls_verified"`
}

func LogAccess(ctx RequestContext) {
	entry := AccessLogEntry{
		Timestamp:            time.Now().UTC().Format(time.RFC3339Nano),
		RequestID:            defaultString(ctx.RequestID, "none"),
		Method:               ctx.Method,
		Host:                 ctx.Host,
		Path:                 ctx.Path,
		RouteID:              defaultString(ctx.RouteID, "none"),
		PoolKey:              defaultString(ctx.PoolKey, "none"),
		UpstreamAddr:         defaultString(ctx.UpstreamAddr, "none"),
		Status:               ctx.Status,
		DurationMS:           ctx.Duration.Milliseconds(),
		BytesIn:              ctx.BytesIn,
		BytesOut:             ctx.BytesOut,
		ErrorCategory:        defaultString(ctx.ErrorCategory, "none"),
		RetryCount:           ctx.RetryCount,
		RetryLastReason:      defaultString(ctx.RetryLastReason, "none"),
		RetryBudgetExhausted: ctx.RetryBudgetExhausted,
		CacheStatus:          defaultString(ctx.CacheStatus, "bypass"),
		SnapshotVersion:      defaultString(ctx.SnapshotVersion, "none"),
		TrafficVariant:       defaultString(ctx.TrafficVariant, "stable"),
		CohortMode:           defaultString(ctx.CohortMode, "random"),
		CohortKeyPresent:     ctx.CohortKeyPresent,
		OverloadRejected:     ctx.OverloadRejected,
		AutoDrainActive:      ctx.AutoDrainActive,
		UserAgent:            ctx.UserAgent,
		RemoteAddr:           ctx.RemoteAddr,
		BreakerState:         defaultString(ctx.BreakerState, "none"),
		BreakerDenied:        ctx.BreakerDenied,
		OutlierIgnored:       ctx.OutlierIgnored,
		EndpointEjected:      ctx.EndpointEjected,
		TLS:                  ctx.TLS,
		MTLSRouteRequired:    ctx.MTLSRouteRequired,
		MTLSVerified:         ctx.MTLSVerified,
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
