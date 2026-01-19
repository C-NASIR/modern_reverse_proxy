package obs

import "time"

type RequestContext struct {
	RequestID       string
	Method          string
	Host            string
	Path            string
	RouteID         string
	PoolKey         string
	UpstreamAddr    string
	Status          int
	Duration        time.Duration
	BytesIn         int64
	BytesOut        int64
	ErrorCategory   string
	SnapshotVersion string
	SnapshotSource  string
	UserAgent       string
	RemoteAddr      string
}
