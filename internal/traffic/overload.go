package traffic

import (
	"context"
	"time"
)

type OverloadLimiter struct {
	inflight     chan struct{}
	queue        chan struct{}
	queueTimeout time.Duration
}

func NewOverloadLimiter(maxInflight int, maxQueue int, queueTimeout time.Duration) *OverloadLimiter {
	if maxInflight <= 0 {
		return nil
	}
	limiter := &OverloadLimiter{
		inflight:     make(chan struct{}, maxInflight),
		queueTimeout: queueTimeout,
	}
	if maxQueue > 0 {
		limiter.queue = make(chan struct{}, maxQueue)
	}
	return limiter
}

func (l *OverloadLimiter) Acquire(ctx context.Context) (func(), bool) {
	if l == nil {
		return func() {}, true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if trySend(l.inflight) {
		return func() { <-l.inflight }, true
	}
	if l.queue == nil {
		return nil, false
	}
	if !trySend(l.queue) {
		return nil, false
	}
	if l.queueTimeout <= 0 {
		l.queueTimeout = time.Millisecond
	}
	timer := time.NewTimer(l.queueTimeout)
	defer timer.Stop()
	select {
	case l.inflight <- struct{}{}:
		<-l.queue
		return func() { <-l.inflight }, true
	case <-timer.C:
		<-l.queue
		return nil, false
	case <-ctx.Done():
		<-l.queue
		return nil, false
	}
}

func trySend(ch chan struct{}) bool {
	select {
	case ch <- struct{}{}:
		return true
	default:
		return false
	}
}
