package outlier

import (
	"math"
	"sort"
	"sync"
	"time"
)

type LatencyWindow struct {
	mu    sync.Mutex
	ring  []int64
	idx   int
	count int
}

func NewLatencyWindow(size int) *LatencyWindow {
	if size <= 0 {
		size = 1
	}
	return &LatencyWindow{ring: make([]int64, size)}
}

func (w *LatencyWindow) Record(latency time.Duration) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.ring[w.idx] = latency.Nanoseconds()
	w.idx = (w.idx + 1) % len(w.ring)
	if w.count < len(w.ring) {
		w.count++
	}
	w.mu.Unlock()
}

func (w *LatencyWindow) Snapshot() []int64 {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	values := make([]int64, w.count)
	for i := 0; i < w.count; i++ {
		values[i] = w.ring[i]
	}
	return values
}

func percentile(values []int64, pct float64) int64 {
	if len(values) == 0 {
		return 0
	}
	if pct <= 0 {
		pct = 0
	}
	if pct >= 1 {
		pct = 1
	}

	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})
	idx := int(math.Ceil(pct*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
