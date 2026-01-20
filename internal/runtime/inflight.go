package runtime

import (
	"context"
	"sync"
	"sync/atomic"
)

type InflightTracker struct {
	count  atomic.Int64
	mu     sync.Mutex
	zeroCh chan struct{}
}

func NewInflightTracker() *InflightTracker {
	zeroCh := make(chan struct{})
	close(zeroCh)
	return &InflightTracker{zeroCh: zeroCh}
}

func (t *InflightTracker) Inc() {
	if t == nil {
		return
	}
	if t.count.Add(1) != 1 {
		return
	}
	t.mu.Lock()
	t.zeroCh = make(chan struct{})
	t.mu.Unlock()
}

func (t *InflightTracker) Dec() {
	if t == nil {
		return
	}
	if t.count.Add(-1) != 0 {
		return
	}
	t.mu.Lock()
	close(t.zeroCh)
	t.mu.Unlock()
}

func (t *InflightTracker) Wait(ctx context.Context) error {
	if t == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	t.mu.Lock()
	waitCh := t.zeroCh
	t.mu.Unlock()
	select {
	case <-waitCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
