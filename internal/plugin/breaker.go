package plugin

import (
	"sync"
	"time"
)

type BreakerState string

const (
	BreakerStateClosed   BreakerState = "closed"
	BreakerStateOpen     BreakerState = "open"
	BreakerStateHalfOpen BreakerState = "half_open"
)

type Breaker struct {
	mu               sync.Mutex
	config           BreakerConfig
	state            BreakerState
	consecutiveFails int
	openUntil        time.Time
	halfOpenInFlight int
	halfOpenSuccess  int
}

func NewBreaker(cfg BreakerConfig) *Breaker {
	return &Breaker{config: cfg, state: BreakerStateClosed}
}

func (b *Breaker) UpdateConfig(cfg BreakerConfig) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.config = cfg
	if !cfg.Enabled {
		b.state = BreakerStateClosed
		b.consecutiveFails = 0
		b.halfOpenInFlight = 0
		b.halfOpenSuccess = 0
		b.openUntil = time.Time{}
	}
}

func (b *Breaker) Allow() (BreakerState, bool) {
	if b == nil {
		return BreakerStateClosed, true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.config.Enabled {
		return BreakerStateClosed, true
	}

	now := time.Now()
	switch b.state {
	case BreakerStateClosed:
		return b.state, true
	case BreakerStateOpen:
		if now.Before(b.openUntil) {
			return b.state, false
		}
		b.state = BreakerStateHalfOpen
		b.halfOpenInFlight = 0
		b.halfOpenSuccess = 0
	}

	if b.state == BreakerStateHalfOpen {
		maxProbes := b.config.HalfOpenProbes
		if maxProbes <= 0 {
			maxProbes = 1
		}
		if b.halfOpenInFlight >= maxProbes {
			return b.state, false
		}
		b.halfOpenInFlight++
		return b.state, true
	}

	return BreakerStateClosed, true
}

func (b *Breaker) Report(success bool) BreakerState {
	if b == nil {
		return BreakerStateClosed
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.config.Enabled {
		return BreakerStateClosed
	}

	switch b.state {
	case BreakerStateClosed:
		if success {
			b.consecutiveFails = 0
			return b.state
		}
		b.consecutiveFails++
		threshold := b.config.ConsecutiveFailures
		if threshold <= 0 {
			threshold = 1
		}
		if b.consecutiveFails >= threshold {
			b.state = BreakerStateOpen
			openDuration := b.config.OpenDuration
			if openDuration <= 0 {
				openDuration = time.Second
			}
			b.openUntil = time.Now().Add(openDuration)
			b.halfOpenInFlight = 0
			b.halfOpenSuccess = 0
		}
	case BreakerStateHalfOpen:
		if b.halfOpenInFlight > 0 {
			b.halfOpenInFlight--
		}
		if !success {
			b.state = BreakerStateOpen
			openDuration := b.config.OpenDuration
			if openDuration <= 0 {
				openDuration = time.Second
			}
			b.openUntil = time.Now().Add(openDuration)
			b.consecutiveFails = 0
			b.halfOpenSuccess = 0
			return b.state
		}
		b.halfOpenSuccess++
		maxProbes := b.config.HalfOpenProbes
		if maxProbes <= 0 {
			maxProbes = 1
		}
		if b.halfOpenSuccess >= maxProbes && b.halfOpenInFlight == 0 {
			b.state = BreakerStateClosed
			b.consecutiveFails = 0
			b.halfOpenSuccess = 0
			b.openUntil = time.Time{}
		}
	case BreakerStateOpen:
		return b.state
	}

	return b.state
}
