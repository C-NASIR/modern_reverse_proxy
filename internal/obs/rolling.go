package obs

import (
	"sync"
	"time"
)

type rollingCounter struct {
	mu      sync.Mutex
	buckets []rollingBucket
	window  time.Duration
}

type rollingBucket struct {
	second int64
	total  int
	errors int
}

func newRollingCounter(window time.Duration) *rollingCounter {
	if window <= 0 {
		window = 10 * time.Second
	}
	size := int(window.Seconds())
	if size < 1 {
		size = 1
	}
	return &rollingCounter{buckets: make([]rollingBucket, size), window: window}
}

func (r *rollingCounter) Record(status int) {
	if r == nil {
		return
	}
	now := time.Now().Unix()
	index := int(now % int64(len(r.buckets)))
	r.mu.Lock()
	bucket := r.buckets[index]
	if bucket.second != now {
		bucket = rollingBucket{second: now}
	}
	bucket.total++
	if status >= 500 {
		bucket.errors++
	}
	r.buckets[index] = bucket
	r.mu.Unlock()
}

func (r *rollingCounter) Counts(window time.Duration) (int, int) {
	if r == nil {
		return 0, 0
	}
	if window <= 0 {
		window = r.window
	}
	maxAge := int64(window.Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	now := time.Now().Unix()
	var total int
	var errors int
	r.mu.Lock()
	for _, bucket := range r.buckets {
		if bucket.second == 0 {
			continue
		}
		if now-bucket.second < maxAge {
			total += bucket.total
			errors += bucket.errors
		}
	}
	r.mu.Unlock()
	return total, errors
}
