package outlier

import (
	"sync/atomic"
	"time"
)

type Config struct {
	Enabled                     bool
	ConsecutiveFailures         int
	ErrorRateThresholdPercent   int
	ErrorRateWindow             time.Duration
	MinRequests                 int
	BaseEjectDuration           time.Duration
	MaxEjectDuration            time.Duration
	MaxEjectPercent             int
	LatencyEnabled              bool
	LatencyWindowSize           int
	LatencyEvalInterval         time.Duration
	LatencyMinSamples           int
	LatencyMultiplier           int
	LatencyConsecutiveIntervals int
}

type EndpointState struct {
	ejectUntil          atomic.Int64
	consecutiveFails    atomic.Int32
	ejectCount          atomic.Int32
	lastEjectAt         atomic.Int64
	windowReq           atomic.Int32
	windowFail          atomic.Int32
	windowStart         atomic.Int64
	latencyBadIntervals atomic.Int32
	latencyWindow       *LatencyWindow
}

func NewEndpointState(cfg Config) *EndpointState {
	state := &EndpointState{}
	state.UpdateConfig(cfg)
	return state
}

func (e *EndpointState) UpdateConfig(cfg Config) {
	if cfg.LatencyEnabled {
		if e.latencyWindow == nil || len(e.latencyWindow.ring) != cfg.LatencyWindowSize {
			e.latencyWindow = NewLatencyWindow(cfg.LatencyWindowSize)
		}
	} else {
		e.latencyWindow = nil
		e.latencyBadIntervals.Store(0)
	}
}

func (e *EndpointState) IsEjected(now time.Time) bool {
	until := e.ejectUntil.Load()
	return until > 0 && now.UnixNano() < until
}

func (e *EndpointState) RecordResult(cfg Config, success bool, now time.Time) (bool, string) {
	if !cfg.Enabled {
		return false, ""
	}

	window := cfg.ErrorRateWindow
	if window <= 0 {
		window = time.Second
	}
	start := e.windowStart.Load()
	if start == 0 || now.Sub(time.Unix(0, start)) > window {
		if e.windowStart.CompareAndSwap(start, now.UnixNano()) {
			e.windowReq.Store(0)
			e.windowFail.Store(0)
		}
	}

	e.windowReq.Add(1)
	if !success {
		e.windowFail.Add(1)
		e.consecutiveFails.Add(1)
		if cfg.ConsecutiveFailures > 0 && int(e.consecutiveFails.Load()) >= cfg.ConsecutiveFailures {
			e.consecutiveFails.Store(0)
			if e.eject(cfg, now) {
				return true, "consecutive_fail"
			}
		}
	} else {
		e.consecutiveFails.Store(0)
	}

	minReq := cfg.MinRequests
	if minReq <= 0 {
		minReq = 1
	}
	windowReq := int(e.windowReq.Load())
	if windowReq < minReq {
		return false, ""
	}

	windowFail := int(e.windowFail.Load())
	threshold := cfg.ErrorRateThresholdPercent
	if threshold <= 0 {
		return false, ""
	}
	failRate := (windowFail * 100) / windowReq
	if failRate >= threshold {
		e.windowReq.Store(0)
		e.windowFail.Store(0)
		if e.eject(cfg, now) {
			return true, "error_rate"
		}
	}

	return false, ""
}

func (e *EndpointState) RecordLatency(latency time.Duration) {
	if e == nil || e.latencyWindow == nil {
		return
	}
	e.latencyWindow.Record(latency)
}

func (e *EndpointState) LatencySnapshot() []int64 {
	if e == nil || e.latencyWindow == nil {
		return nil
	}
	return e.latencyWindow.Snapshot()
}

func (e *EndpointState) ObserveLatency(cfg Config, bad bool, now time.Time) (bool, string) {
	if !cfg.Enabled || !cfg.LatencyEnabled {
		return false, ""
	}
	if bad {
		badCount := e.latencyBadIntervals.Add(1)
		if cfg.LatencyConsecutiveIntervals > 0 && int(badCount) >= cfg.LatencyConsecutiveIntervals {
			e.latencyBadIntervals.Store(0)
			if e.eject(cfg, now) {
				return true, "latency"
			}
		}
	} else {
		e.latencyBadIntervals.Store(0)
	}
	return false, ""
}

func (e *EndpointState) eject(cfg Config, now time.Time) bool {
	base := cfg.BaseEjectDuration
	max := cfg.MaxEjectDuration
	if base <= 0 {
		base = time.Second
	}
	if max <= 0 {
		max = base
	}

	lastEject := e.lastEjectAt.Load()
	if lastEject > 0 && now.Sub(time.Unix(0, lastEject)) > 5*time.Minute {
		e.ejectCount.Store(0)
	}

	count := e.ejectCount.Add(1)
	duration := base
	for i := int32(1); i < count; i++ {
		duration *= 2
		if duration >= max {
			duration = max
			break
		}
	}
	if duration > max {
		duration = max
	}

	e.lastEjectAt.Store(now.UnixNano())
	e.ejectUntil.Store(now.Add(duration).UnixNano())
	return true
}
