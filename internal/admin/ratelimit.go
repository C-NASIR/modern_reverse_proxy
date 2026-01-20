package admin

import (
	"net"
	"sync"
	"time"
)

const (
	defaultRateLimitRPS   = 5
	defaultRateLimitBurst = 10
	defaultMaxFailures    = 20
	defaultBlockDuration  = 10 * time.Minute
)

type RateLimitConfig struct {
	RPS           int
	Burst         int
	MaxFailures   int
	BlockDuration time.Duration
}

type RateLimiter struct {
	mu          sync.Mutex
	buckets     map[string]*tokenBucket
	failures    map[string]*failureState
	rate        float64
	burst       float64
	maxFailures int
	blockFor    time.Duration
}

type tokenBucket struct {
	remaining float64
	last      time.Time
}

type failureState struct {
	count       int
	blockedTill time.Time
}

func NewRateLimiter(cfg RateLimitConfig) *RateLimiter {
	rate := cfg.RPS
	if rate <= 0 {
		rate = defaultRateLimitRPS
	}
	burst := cfg.Burst
	if burst <= 0 {
		burst = defaultRateLimitBurst
	}
	maxFailures := cfg.MaxFailures
	if maxFailures <= 0 {
		maxFailures = defaultMaxFailures
	}
	blockFor := cfg.BlockDuration
	if blockFor <= 0 {
		blockFor = defaultBlockDuration
	}

	return &RateLimiter{
		buckets:     make(map[string]*tokenBucket),
		failures:    make(map[string]*failureState),
		rate:        float64(rate),
		burst:       float64(burst),
		maxFailures: maxFailures,
		blockFor:    blockFor,
	}
}

func (l *RateLimiter) Allow(addr string) bool {
	if l == nil {
		return true
	}
	ip := clientIP(addr)
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	state := l.failures[ip]
	if state != nil && now.Before(state.blockedTill) {
		return false
	}

	bucket := l.buckets[ip]
	if bucket == nil {
		bucket = &tokenBucket{remaining: l.burst, last: now}
		l.buckets[ip] = bucket
	}

	elapsed := now.Sub(bucket.last).Seconds()
	bucket.remaining = minFloat(l.burst, bucket.remaining+elapsed*l.rate)
	bucket.last = now

	if bucket.remaining < 1 {
		return false
	}
	bucket.remaining -= 1
	return true
}

func (l *RateLimiter) RecordFailure(addr string) {
	if l == nil {
		return
	}
	ip := clientIP(addr)
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	state := l.failures[ip]
	if state == nil {
		state = &failureState{}
		l.failures[ip] = state
	}
	if now.Before(state.blockedTill) {
		return
	}
	state.count++
	if state.count >= l.maxFailures {
		state.blockedTill = now.Add(l.blockFor)
	}
}

func (l *RateLimiter) ResetFailures(addr string) {
	if l == nil {
		return
	}
	ip := clientIP(addr)

	l.mu.Lock()
	defer l.mu.Unlock()

	if state := l.failures[ip]; state != nil {
		state.count = 0
	}
}

func clientIP(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func minFloat(a float64, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
