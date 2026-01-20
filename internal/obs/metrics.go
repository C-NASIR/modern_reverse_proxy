package obs

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type MetricsConfig struct {
	RouteTopK         int
	PoolTopK          int
	RecomputeInterval time.Duration
}

type Metrics struct {
	registry               *prometheus.Registry
	topk                   *TopK
	requests               *prometheus.CounterVec
	upstreamErrors         *prometheus.CounterVec
	proxyErrors            *prometheus.CounterVec
	retries                *prometheus.CounterVec
	retryBudgetExhausted   *prometheus.CounterVec
	configApply            *prometheus.CounterVec
	configApplyDuration    prometheus.Histogram
	configConflicts        prometheus.Counter
	circuitOpen            *prometheus.CounterVec
	outlierEjections       *prometheus.CounterVec
	outlierFailOpen        *prometheus.CounterVec
	mtlsReject             *prometheus.CounterVec
	cacheRequests          *prometheus.CounterVec
	cacheCoalesceBreakaway *prometheus.CounterVec
	cacheStoreFail         *prometheus.CounterVec
	variantRequests        *prometheus.CounterVec
	variantErrors          *prometheus.CounterVec
	overloadRejects        *prometheus.CounterVec
	pluginCalls            *prometheus.CounterVec
	pluginBypass           *prometheus.CounterVec
	pluginShortCircuit     *prometheus.CounterVec
	pluginFailClosed       *prometheus.CounterVec
	requestDuration        *prometheus.HistogramVec
	upstreamRoundTrip      *prometheus.HistogramVec
	snapshotInfo           *prometheus.GaugeVec
	breakerOpen            *prometheus.GaugeVec
	bundleVerify           *prometheus.CounterVec
	rolloutStage           *prometheus.CounterVec
	rollbackTotal          *prometheus.CounterVec
	requestWindow          *rollingCounter
	mu                     sync.Mutex
	lastVersion            string
	lastSource             string
}

var (
	defaultMetricsMu sync.RWMutex
	defaultMetrics   *Metrics
)

func SetDefaultMetrics(metrics *Metrics) {
	defaultMetricsMu.Lock()
	defaultMetrics = metrics
	defaultMetricsMu.Unlock()
}

func DefaultMetrics() *Metrics {
	defaultMetricsMu.RLock()
	defer defaultMetricsMu.RUnlock()
	return defaultMetrics
}

func NewMetrics(cfg MetricsConfig) *Metrics {
	registry := prometheus.NewRegistry()
	topk := NewTopK(cfg.RouteTopK, cfg.PoolTopK, cfg.RecomputeInterval)

	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_requests_total",
		Help: "Total proxy requests",
	}, []string{"route", "status_class"})

	upstreamErrors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_upstream_errors_total",
		Help: "Total upstream errors",
	}, []string{"pool", "category"})

	proxyErrors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_proxy_errors_total",
		Help: "Total proxy-generated errors",
	}, []string{"route", "category"})

	retries := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_retries_total",
		Help: "Total proxy retries",
	}, []string{"route", "reason"})

	retryBudgetExhausted := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_retry_budget_exhausted_total",
		Help: "Total retry budget exhaustion events",
	}, []string{"route"})

	configApply := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_config_apply_total",
		Help: "Total config apply attempts",
	}, []string{"result"})

	configApplyDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "proxy_config_apply_duration_seconds",
		Help:    "Config apply duration",
		Buckets: prometheus.DefBuckets,
	})

	configConflicts := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxy_config_conflict_total",
		Help: "Total config provider conflicts",
	})

	circuitOpen := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_circuit_open_total",
		Help: "Total circuit breaker open rejections",
	}, []string{"pool"})

	outlierEjections := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_outlier_ejections_total",
		Help: "Total outlier ejections",
	}, []string{"pool", "reason"})

	outlierFailOpen := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_outlier_fail_open_total",
		Help: "Total outlier fail-open events",
	}, []string{"pool"})

	mtlsReject := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_mtls_reject_total",
		Help: "Total mTLS route rejections",
	}, []string{"route"})

	cacheRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_cache_requests_total",
		Help: "Total cache lookups",
	}, []string{"route", "status"})

	cacheCoalesceBreakaway := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_cache_coalesce_breakaway_total",
		Help: "Total cache coalesce breakaway events",
	}, []string{"route"})

	cacheStoreFail := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_cache_store_fail_total",
		Help: "Total cache store failures",
	}, []string{"route"})

	variantRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_variant_requests_total",
		Help: "Total requests per traffic variant",
	}, []string{"route", "variant"})

	variantErrors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_variant_errors_total",
		Help: "Total errors per traffic variant",
	}, []string{"route", "variant"})

	overloadRejects := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_overload_reject_total",
		Help: "Total overload rejections",
	}, []string{"route"})

	pluginCalls := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_plugin_calls_total",
		Help: "Total plugin calls",
	}, []string{"filter", "phase", "result"})

	pluginBypass := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_plugin_bypass_total",
		Help: "Total plugin bypasses",
	}, []string{"filter", "reason"})

	pluginShortCircuit := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_plugin_shortcircuit_total",
		Help: "Total plugin short-circuits",
	}, []string{"filter"})

	pluginFailClosed := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_plugin_failclosed_total",
		Help: "Total plugin fail-closed responses",
	}, []string{"filter"})

	requestDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "proxy_request_duration_seconds",
		Help:    "Proxy request duration",
		Buckets: prometheus.DefBuckets,
	}, []string{"route"})

	upstreamRoundTrip := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "proxy_upstream_roundtrip_seconds",
		Help:    "Upstream roundtrip duration",
		Buckets: prometheus.DefBuckets,
	}, []string{"pool"})

	bundleVerify := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_bundle_verify_total",
		Help: "Total bundle verification attempts",
	}, []string{"result"})

	rolloutStage := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_rollout_stage_total",
		Help: "Total rollout stage events",
	}, []string{"stage", "result"})

	rollbackTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_rollback_total",
		Help: "Total rollback attempts",
	}, []string{"result"})

	breakerOpen := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "proxy_breaker_open",
		Help: "Breaker open state",
	}, []string{"pool"})

	snapshotInfoGauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "proxy_active_snapshot_info",
		Help: "Active snapshot metadata",
	}, []string{"version", "source"})

	registry.MustRegister(requests, upstreamErrors, proxyErrors, retries, retryBudgetExhausted, configApply, configApplyDuration, configConflicts, circuitOpen, outlierEjections, outlierFailOpen, mtlsReject, cacheRequests, cacheCoalesceBreakaway, cacheStoreFail, variantRequests, variantErrors, overloadRejects, pluginCalls, pluginBypass, pluginShortCircuit, pluginFailClosed, requestDuration, upstreamRoundTrip, snapshotInfoGauge, breakerOpen, bundleVerify, rolloutStage, rollbackTotal)

	return &Metrics{
		registry:               registry,
		topk:                   topk,
		requests:               requests,
		upstreamErrors:         upstreamErrors,
		proxyErrors:            proxyErrors,
		retries:                retries,
		retryBudgetExhausted:   retryBudgetExhausted,
		configApply:            configApply,
		configApplyDuration:    configApplyDuration,
		configConflicts:        configConflicts,
		circuitOpen:            circuitOpen,
		outlierEjections:       outlierEjections,
		outlierFailOpen:        outlierFailOpen,
		mtlsReject:             mtlsReject,
		cacheRequests:          cacheRequests,
		cacheCoalesceBreakaway: cacheCoalesceBreakaway,
		cacheStoreFail:         cacheStoreFail,
		variantRequests:        variantRequests,
		variantErrors:          variantErrors,
		overloadRejects:        overloadRejects,
		pluginCalls:            pluginCalls,
		pluginBypass:           pluginBypass,
		pluginShortCircuit:     pluginShortCircuit,
		pluginFailClosed:       pluginFailClosed,
		requestDuration:        requestDuration,
		upstreamRoundTrip:      upstreamRoundTrip,
		snapshotInfo:           snapshotInfoGauge,
		breakerOpen:            breakerOpen,
		bundleVerify:           bundleVerify,
		rolloutStage:           rolloutStage,
		rollbackTotal:          rollbackTotal,
		requestWindow:          newRollingCounter(10 * time.Second),
	}
}

func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		})
	}
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *Metrics) ObserveRequest(routeID string, poolKey string, status int, duration time.Duration) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	m.topk.ObserveHit(routeID, poolKey)
	canonRoute := m.topk.CanonRoute(routeID)
	statusClass := statusClass(status)
	m.requests.WithLabelValues(canonRoute, statusClass).Inc()
	m.requestDuration.WithLabelValues(canonRoute).Observe(duration.Seconds())
	m.requestWindow.Record(status)
}

func (m *Metrics) Canonicalize(routeID string, poolKey string) (string, string) {
	if m == nil {
		return "none", "none"
	}
	defer func() {
		_ = recover()
	}()

	m.topk.ObserveHit(routeID, poolKey)
	return m.topk.CanonRoute(routeID), m.topk.CanonPool(poolKey)
}

func (m *Metrics) ObserveRequestCanonical(canonRoute string, status int, duration time.Duration) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	if canonRoute == "" {
		canonRoute = "none"
	}
	statusClass := statusClass(status)
	m.requests.WithLabelValues(canonRoute, statusClass).Inc()
	m.requestDuration.WithLabelValues(canonRoute).Observe(duration.Seconds())
	m.requestWindow.Record(status)
}

func (m *Metrics) ObserveUpstreamRoundTrip(poolKey string, duration time.Duration) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	m.topk.ObserveHit("", poolKey)
	canonPool := m.topk.CanonPool(poolKey)
	m.upstreamRoundTrip.WithLabelValues(canonPool).Observe(duration.Seconds())
}

func (m *Metrics) RecordUpstreamError(poolKey string, category string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	m.topk.ObserveHit("", poolKey)
	canonPool := m.topk.CanonPool(poolKey)
	m.upstreamErrors.WithLabelValues(canonPool, category).Inc()
}

func (m *Metrics) RecordProxyError(routeID string, category string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	canonRoute := m.topk.CanonRoute(routeID)
	m.proxyErrors.WithLabelValues(canonRoute, category).Inc()
}

func (m *Metrics) RecordProxyErrorCanonical(canonRoute string, category string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	if canonRoute == "" {
		canonRoute = "none"
	}
	m.proxyErrors.WithLabelValues(canonRoute, category).Inc()
}

func (m *Metrics) RecordRetry(routeID string, reason string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	m.topk.ObserveHit(routeID, "")
	canonRoute := m.topk.CanonRoute(routeID)
	if reason == "" {
		reason = "unknown"
	}
	m.retries.WithLabelValues(canonRoute, reason).Inc()
}

func (m *Metrics) RecordRetryBudgetExhausted(routeID string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	m.topk.ObserveHit(routeID, "")
	canonRoute := m.topk.CanonRoute(routeID)
	m.retryBudgetExhausted.WithLabelValues(canonRoute).Inc()
}

func (m *Metrics) RecordConfigApply(result string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	m.configApply.WithLabelValues(result).Inc()
}

func (m *Metrics) RecordConfigApplyDuration(duration time.Duration) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	m.configApplyDuration.Observe(duration.Seconds())
}

func (m *Metrics) RecordConfigConflict() {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	m.configConflicts.Inc()
}

func (m *Metrics) RecordCircuitOpen(poolKey string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	m.topk.ObserveHit("", poolKey)
	canonPool := m.topk.CanonPool(poolKey)
	m.circuitOpen.WithLabelValues(canonPool).Inc()
}

func (m *Metrics) RecordOutlierEjection(poolKey string, reason string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	m.topk.ObserveHit("", poolKey)
	canonPool := m.topk.CanonPool(poolKey)
	if reason == "" {
		reason = "unknown"
	}
	m.outlierEjections.WithLabelValues(canonPool, reason).Inc()
}

func (m *Metrics) RecordOutlierFailOpen(poolKey string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	m.topk.ObserveHit("", poolKey)
	canonPool := m.topk.CanonPool(poolKey)
	m.outlierFailOpen.WithLabelValues(canonPool).Inc()
}

func (m *Metrics) RecordMTLSReject(routeID string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	canonRoute := m.topk.CanonRoute(routeID)
	m.mtlsReject.WithLabelValues(canonRoute).Inc()
}

func (m *Metrics) RecordMTLSRejectCanonical(canonRoute string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	if canonRoute == "" {
		canonRoute = "none"
	}
	m.mtlsReject.WithLabelValues(canonRoute).Inc()
}

func (m *Metrics) RecordCacheRequest(routeID string, status string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	if status == "" {
		status = "unknown"
	}
	m.topk.ObserveHit(routeID, "")
	canonRoute := m.topk.CanonRoute(routeID)
	m.cacheRequests.WithLabelValues(canonRoute, status).Inc()
}

func (m *Metrics) RecordCacheRequestCanonical(canonRoute string, status string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	if canonRoute == "" {
		canonRoute = "none"
	}
	if status == "" {
		status = "unknown"
	}
	m.cacheRequests.WithLabelValues(canonRoute, status).Inc()
}

func (m *Metrics) RecordCacheCoalesceBreakaway(routeID string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	m.topk.ObserveHit(routeID, "")
	canonRoute := m.topk.CanonRoute(routeID)
	m.cacheCoalesceBreakaway.WithLabelValues(canonRoute).Inc()
}

func (m *Metrics) RecordCacheCoalesceBreakawayCanonical(canonRoute string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	if canonRoute == "" {
		canonRoute = "none"
	}
	m.cacheCoalesceBreakaway.WithLabelValues(canonRoute).Inc()
}

func (m *Metrics) RecordCacheStoreFail(routeID string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	m.topk.ObserveHit(routeID, "")
	canonRoute := m.topk.CanonRoute(routeID)
	m.cacheStoreFail.WithLabelValues(canonRoute).Inc()
}

func (m *Metrics) RecordCacheStoreFailCanonical(canonRoute string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	if canonRoute == "" {
		canonRoute = "none"
	}
	m.cacheStoreFail.WithLabelValues(canonRoute).Inc()
}

func (m *Metrics) RecordVariantRequest(routeID string, variant string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	if variant == "" {
		variant = "unknown"
	}
	m.topk.ObserveHit(routeID, "")
	canonRoute := m.topk.CanonRoute(routeID)
	m.variantRequests.WithLabelValues(canonRoute, variant).Inc()
}

func (m *Metrics) RecordVariantRequestCanonical(canonRoute string, variant string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	if canonRoute == "" {
		canonRoute = "none"
	}
	if variant == "" {
		variant = "stable"
	}
	m.variantRequests.WithLabelValues(canonRoute, variant).Inc()
}

func (m *Metrics) RecordVariantError(routeID string, variant string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	if variant == "" {
		variant = "unknown"
	}
	m.topk.ObserveHit(routeID, "")
	canonRoute := m.topk.CanonRoute(routeID)
	m.variantErrors.WithLabelValues(canonRoute, variant).Inc()
}

func (m *Metrics) RecordVariantErrorCanonical(canonRoute string, variant string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	if canonRoute == "" {
		canonRoute = "none"
	}
	if variant == "" {
		variant = "stable"
	}
	m.variantErrors.WithLabelValues(canonRoute, variant).Inc()
}

func (m *Metrics) RecordOverloadReject(routeID string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	m.topk.ObserveHit(routeID, "")
	canonRoute := m.topk.CanonRoute(routeID)
	m.overloadRejects.WithLabelValues(canonRoute).Inc()
}

func (m *Metrics) RecordOverloadRejectCanonical(canonRoute string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	if canonRoute == "" {
		canonRoute = "none"
	}
	m.overloadRejects.WithLabelValues(canonRoute).Inc()
}

func (m *Metrics) RecordBundleVerify(result string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	if result == "" {
		result = "unknown"
	}
	m.bundleVerify.WithLabelValues(result).Inc()
}

func (m *Metrics) RecordRolloutStage(stage string, result string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	if stage == "" {
		stage = "unknown"
	}
	if result == "" {
		result = "unknown"
	}
	m.rolloutStage.WithLabelValues(stage, result).Inc()
}

func (m *Metrics) RecordRollback(result string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	if result == "" {
		result = "unknown"
	}
	m.rollbackTotal.WithLabelValues(result).Inc()
}

func (m *Metrics) Rolling5xx(window time.Duration) (int, int) {
	if m == nil || m.requestWindow == nil {
		return 0, 0
	}
	return m.requestWindow.Counts(window)
}

func (m *Metrics) RecordPluginCall(filter string, phase string, result string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	if filter == "" {
		filter = "unknown"
	}
	if phase == "" {
		phase = "unknown"
	}
	if result == "" {
		result = "unknown"
	}
	m.pluginCalls.WithLabelValues(filter, phase, result).Inc()
}

func (m *Metrics) RecordPluginBypass(filter string, reason string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	if filter == "" {
		filter = "unknown"
	}
	if reason == "" {
		reason = "unknown"
	}
	m.pluginBypass.WithLabelValues(filter, reason).Inc()
}

func (m *Metrics) RecordPluginShortCircuit(filter string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	if filter == "" {
		filter = "unknown"
	}
	m.pluginShortCircuit.WithLabelValues(filter).Inc()
}

func (m *Metrics) RecordPluginFailClosed(filter string) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	if filter == "" {
		filter = "unknown"
	}
	m.pluginFailClosed.WithLabelValues(filter).Inc()
}

func (m *Metrics) SetBreakerOpen(poolKey string, open bool) {
	if m == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	m.topk.ObserveHit("", poolKey)
	canonPool := m.topk.CanonPool(poolKey)
	value := 0.0
	if open {
		value = 1.0
	}
	m.breakerOpen.WithLabelValues(canonPool).Set(value)
}

func (m *Metrics) SetSnapshotInfo(version string, source string) {
	if m == nil || version == "" {
		return
	}
	if source == "" {
		source = "unknown"
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastVersion != "" {
		m.snapshotInfo.WithLabelValues(m.lastVersion, m.lastSource).Set(0)
	}
	m.snapshotInfo.WithLabelValues(version, source).Set(1)
	m.lastVersion = version
	m.lastSource = source
}

func statusClass(status int) string {
	if status <= 0 {
		return "unknown"
	}
	class := status / 100
	return fmt.Sprintf("%dxx", class)
}
