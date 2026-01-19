package breaker

import (
	"errors"
	"sync/atomic"
	"time"
)

type State int32

const (
	StateClosed State = iota + 1
	StateOpen
	StateHalfOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

type Config struct {
	Enabled                     bool
	FailureRateThresholdPercent int
	MinimumRequests             int
	EvaluationWindow            time.Duration
	OpenDuration                time.Duration
	HalfOpenMaxProbes           int
}

type Breaker struct {
	state         atomic.Int32
	reqCount      atomic.Int32
	failCount     atomic.Int32
	windowStart   atomic.Int64
	openUntil     atomic.Int64
	probeInFlight atomic.Int32
	probeSuccess  atomic.Int32
	probeFail     atomic.Int32
	config        atomic.Value
}

func New(cfg Config) *Breaker {
	b := &Breaker{}
	b.state.Store(int32(StateClosed))
	b.windowStart.Store(time.Now().UnixNano())
	b.config.Store(cfg)
	return b
}

func (b *Breaker) UpdateConfig(cfg Config) {
	if b == nil {
		return
	}
	b.config.Store(cfg)
}

func (b *Breaker) Allow() (State, bool, error) {
	if b == nil {
		return StateClosed, true, errors.New("breaker is nil")
	}
	cfg, ok := b.loadConfig()
	if !ok {
		return StateClosed, true, errors.New("breaker config missing")
	}
	if !cfg.Enabled {
		return StateClosed, true, nil
	}

	now := time.Now()
	state := State(b.state.Load())
	if state == StateClosed {
		return state, true, nil
	}
	if state == StateOpen {
		if now.UnixNano() < b.openUntil.Load() {
			return state, false, nil
		}
		if b.state.CompareAndSwap(int32(StateOpen), int32(StateHalfOpen)) {
			b.resetProbes()
		}
		state = State(b.state.Load())
	}
	if state == StateHalfOpen {
		return b.allowHalfOpen(cfg)
	}
	return StateClosed, true, errors.New("breaker state unknown")
}

func (b *Breaker) Report(success bool) (State, error) {
	if b == nil {
		return StateClosed, errors.New("breaker is nil")
	}
	cfg, ok := b.loadConfig()
	if !ok {
		return StateClosed, errors.New("breaker config missing")
	}
	if !cfg.Enabled {
		return StateClosed, nil
	}

	now := time.Now()
	state := State(b.state.Load())
	switch state {
	case StateClosed:
		b.rotateWindow(cfg, now)
		b.reqCount.Add(1)
		if !success {
			b.failCount.Add(1)
		}
		b.maybeOpen(cfg, now)
	case StateHalfOpen:
		if b.probeInFlight.Load() > 0 {
			b.probeInFlight.Add(-1)
		}
		if !success {
			b.probeFail.Add(1)
			b.open(now, cfg)
			return StateOpen, nil
		}
		b.probeSuccess.Add(1)
		if b.probeFail.Load() == 0 && int(b.probeSuccess.Load()) >= cfg.HalfOpenMaxProbes {
			b.close(now)
		}
	case StateOpen:
		return state, nil
	default:
		return StateClosed, errors.New("breaker state unknown")
	}

	return State(b.state.Load()), nil
}

func (b *Breaker) loadConfig() (Config, bool) {
	value := b.config.Load()
	if value == nil {
		return Config{}, false
	}
	config, ok := value.(Config)
	return config, ok
}

func (b *Breaker) allowHalfOpen(cfg Config) (State, bool, error) {
	maxProbes := cfg.HalfOpenMaxProbes
	if maxProbes <= 0 {
		maxProbes = 1
	}
	if b.probeInFlight.Add(1) > int32(maxProbes) {
		b.probeInFlight.Add(-1)
		return StateHalfOpen, false, nil
	}
	return StateHalfOpen, true, nil
}

func (b *Breaker) rotateWindow(cfg Config, now time.Time) {
	window := cfg.EvaluationWindow
	if window <= 0 {
		window = time.Second
	}
	start := b.windowStart.Load()
	if start == 0 || now.Sub(time.Unix(0, start)) > window {
		if b.windowStart.CompareAndSwap(start, now.UnixNano()) {
			b.reqCount.Store(0)
			b.failCount.Store(0)
		}
	}
}

func (b *Breaker) maybeOpen(cfg Config, now time.Time) {
	minRequests := cfg.MinimumRequests
	if minRequests <= 0 {
		minRequests = 1
	}
	reqCount := int(b.reqCount.Load())
	if reqCount < minRequests {
		return
	}

	failCount := int(b.failCount.Load())
	if reqCount == 0 {
		return
	}
	threshold := cfg.FailureRateThresholdPercent
	if threshold <= 0 {
		return
	}
	failureRate := (failCount * 100) / reqCount
	if failureRate >= threshold {
		b.open(now, cfg)
	}
}

func (b *Breaker) open(now time.Time, cfg Config) {
	openFor := cfg.OpenDuration
	if openFor <= 0 {
		openFor = time.Second
	}
	b.openUntil.Store(now.Add(openFor).UnixNano())
	b.state.Store(int32(StateOpen))
}

func (b *Breaker) close(now time.Time) {
	b.state.Store(int32(StateClosed))
	b.windowStart.Store(now.UnixNano())
	b.reqCount.Store(0)
	b.failCount.Store(0)
	b.resetProbes()
}

func (b *Breaker) resetProbes() {
	b.probeInFlight.Store(0)
	b.probeSuccess.Store(0)
	b.probeFail.Store(0)
}
