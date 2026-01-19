package traffic

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

type Stats struct {
	mu             sync.Mutex
	buckets        []bucket
	bucketDuration time.Duration
	window         time.Duration
	current        int
	currentStart   time.Time
}

type bucket struct {
	start     time.Time
	stableReq int64
	stableErr int64
	canaryReq int64
	canaryErr int64
}

type WindowTotals struct {
	StableReq int64
	StableErr int64
	CanaryReq int64
	CanaryErr int64
}

func NewStats(window time.Duration) *Stats {
	if window <= 0 {
		window = time.Minute
	}
	bucketDuration := time.Second
	if window < time.Second {
		bucketDuration = window
	}
	count := int(math.Ceil(float64(window) / float64(bucketDuration)))
	if count < 1 {
		count = 1
	}
	return &Stats{
		buckets:        make([]bucket, count),
		bucketDuration: bucketDuration,
		window:         window,
	}
}

func (s *Stats) Record(variant Variant, status int, proxyError bool) {
	if s == nil {
		return
	}
	isError := proxyError || status >= 500
	now := time.Now()

	s.mu.Lock()
	s.rotateLocked(now)
	b := &s.buckets[s.current]
	switch variant {
	case VariantCanary:
		b.canaryReq++
		if isError {
			b.canaryErr++
		}
	default:
		b.stableReq++
		if isError {
			b.stableErr++
		}
	}
	s.mu.Unlock()
}

func (s *Stats) WindowTotals(now time.Time) WindowTotals {
	if s == nil {
		return WindowTotals{}
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	s.rotateLocked(now)
	cutoff := now.Add(-s.window)
	var totals WindowTotals
	for i := range s.buckets {
		b := &s.buckets[i]
		if b.start.IsZero() || b.start.Before(cutoff) {
			continue
		}
		totals.StableReq += b.stableReq
		totals.StableErr += b.stableErr
		totals.CanaryReq += b.canaryReq
		totals.CanaryErr += b.canaryErr
	}
	s.mu.Unlock()
	return totals
}

func (s *Stats) rotateLocked(now time.Time) {
	if s.currentStart.IsZero() {
		s.currentStart = now
		s.buckets[s.current].start = now
		return
	}
	if now.Sub(s.currentStart) < s.bucketDuration {
		return
	}
	steps := int(now.Sub(s.currentStart) / s.bucketDuration)
	if steps >= len(s.buckets) {
		for i := range s.buckets {
			s.buckets[i] = bucket{}
		}
		s.current = 0
		s.currentStart = now
		s.buckets[0].start = now
		return
	}
	for i := 0; i < steps; i++ {
		s.current = (s.current + 1) % len(s.buckets)
		s.buckets[s.current] = bucket{start: s.currentStart.Add(s.bucketDuration * time.Duration(i+1))}
	}
	s.currentStart = s.currentStart.Add(s.bucketDuration * time.Duration(steps))
}

type AutoDrainConfig struct {
	Enabled             bool
	Window              time.Duration
	MinRequests         int
	ErrorRateMultiplier float64
	Cooloff             time.Duration
}

type AutoDrain struct {
	stats        *Stats
	config       AutoDrainConfig
	drainedUntil atomic.Int64
	stopCh       chan struct{}
}

func NewAutoDrain(stats *Stats, cfg AutoDrainConfig) *AutoDrain {
	if stats == nil {
		return nil
	}
	controller := &AutoDrain{
		stats:  stats,
		config: cfg,
		stopCh: make(chan struct{}),
	}
	go controller.loop()
	return controller
}

func (a *AutoDrain) Active() bool {
	if a == nil {
		return false
	}
	until := a.drainedUntil.Load()
	return until > 0 && time.Now().UnixNano() < until
}

func (a *AutoDrain) Stop() {
	if a == nil {
		return
	}
	select {
	case <-a.stopCh:
		return
	default:
		close(a.stopCh)
	}
}

func (a *AutoDrain) loop() {
	interval := a.stats.bucketDuration
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.evaluate(time.Now())
		case <-a.stopCh:
			return
		}
	}
}

func (a *AutoDrain) evaluate(now time.Time) {
	if a == nil || a.stats == nil {
		return
	}
	totals := a.stats.WindowTotals(now)
	if totals.CanaryReq < int64(a.config.MinRequests) {
		return
	}
	if totals.CanaryReq == 0 {
		return
	}
	canaryRate := float64(totals.CanaryErr) / float64(totals.CanaryReq)
	if canaryRate <= 0 {
		return
	}
	if totals.StableReq == 0 {
		a.drainUntil(now)
		return
	}
	stableRate := float64(totals.StableErr) / float64(totals.StableReq)
	if canaryRate >= a.config.ErrorRateMultiplier*stableRate {
		a.drainUntil(now)
	}
}

func (a *AutoDrain) drainUntil(now time.Time) {
	if a.config.Cooloff <= 0 {
		return
	}
	until := now.Add(a.config.Cooloff).UnixNano()
	for {
		current := a.drainedUntil.Load()
		if current >= until {
			return
		}
		if a.drainedUntil.CompareAndSwap(current, until) {
			return
		}
	}
}
