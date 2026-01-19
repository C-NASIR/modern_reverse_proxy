package retry

import "sync"

type Budget struct {
	mu          sync.Mutex
	percent     float64
	burst       int
	tokens      int
	accumulator float64
}

func NewBudget(percent int, burst int) *Budget {
	return &Budget{
		percent: float64(percent) / 100,
		burst:   burst,
	}
}

func (b *Budget) RecordSuccess() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.percent <= 0 || b.burst <= 0 {
		return
	}
	b.accumulator += b.percent
	if b.accumulator < 1 {
		return
	}
	add := int(b.accumulator)
	b.accumulator -= float64(add)
	b.tokens += add
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
}

func (b *Budget) Consume() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}
