package obs

import "time"

type RequestContext struct {
	RequestID            string
	Method               string
	Host                 string
	Path                 string
	RouteID              string
	PoolKey              string
	UpstreamAddr         string
	PluginFilters        []string
	PluginBypassed       bool
	PluginBypassReason   string
	PluginFailureMode    string
	PluginShortCircuit   bool
	PluginMutationDenied bool
	Status               int
	Duration             time.Duration
	BytesIn              int64
	BytesOut             int64
	ErrorCategory        string
	RetryCount           int
	RetryLastReason      string
	RetryBudgetExhausted bool
	CacheStatus          string
	SnapshotVersion      string
	SnapshotSource       string
	TrafficVariant       string
	CohortMode           string
	CohortKeyPresent     bool
	OverloadRejected     bool
	AutoDrainActive      bool
	UserAgent            string
	RemoteAddr           string
	BreakerState         string
	BreakerDenied        bool
	OutlierIgnored       bool
	EndpointEjected      bool
	TLS                  bool
	MTLSRouteRequired    bool
	MTLSVerified         bool
}
