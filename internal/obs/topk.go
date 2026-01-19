package obs

import (
	"sort"
	"sync"
	"time"
)

const (
	defaultRouteTopK       = 200
	defaultPoolTopK        = 200
	defaultRecomputeInterval = 10 * time.Second
)

type TopK struct {
	mu          sync.Mutex
	routeCounts map[string]int64
	poolCounts  map[string]int64
	routeTop    map[string]struct{}
	poolTop     map[string]struct{}
	routeK      int
	poolK       int
	interval    time.Duration
	lastRecompute time.Time
}

func NewTopK(routeK int, poolK int, interval time.Duration) *TopK {
	if routeK <= 0 {
		routeK = defaultRouteTopK
	}
	if poolK <= 0 {
		poolK = defaultPoolTopK
	}
	if interval <= 0 {
		interval = defaultRecomputeInterval
	}

	t := &TopK{
		routeCounts: make(map[string]int64),
		poolCounts:  make(map[string]int64),
		routeTop:    make(map[string]struct{}),
		poolTop:     make(map[string]struct{}),
		routeK:      routeK,
		poolK:       poolK,
		interval:    interval,
		lastRecompute: time.Time{},
	}
	go t.recomputeLoop()
	return t
}

func (t *TopK) ObserveHit(routeID string, poolKey string) {
	if t == nil {
		return
	}
	defer func() {
		_ = recover()
	}()

	t.mu.Lock()
	defer t.mu.Unlock()
	if routeID != "" && routeID != "none" {
		t.routeCounts[routeID]++
		if len(t.routeTop) < t.routeK {
			t.routeTop[routeID] = struct{}{}
		}
	}
	if poolKey != "" && poolKey != "none" {
		t.poolCounts[poolKey]++
		if len(t.poolTop) < t.poolK {
			t.poolTop[poolKey] = struct{}{}
		}
	}
	if time.Since(t.lastRecompute) >= t.interval {
		t.recomputeLocked()
		t.lastRecompute = time.Now()
	}
}

func (t *TopK) CanonRoute(routeID string) string {
	if routeID == "" || routeID == "none" {
		return "none"
	}
	return t.canon(routeID, func() map[string]struct{} { return t.routeTop })
}

func (t *TopK) CanonPool(poolKey string) string {
	if poolKey == "" || poolKey == "none" {
		return "none"
	}
	return t.canon(poolKey, func() map[string]struct{} { return t.poolTop })
}

func (t *TopK) canon(value string, getTop func() map[string]struct{}) (result string) {
	result = "other"
	if t == nil {
		return result
	}
	defer func() {
		if recover() != nil {
			result = "other"
		}
	}()

	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := getTop()[value]; ok {
		return value
	}
	return result
}

func (t *TopK) recomputeLoop() {
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()

	for {
		<-ticker.C
		t.recompute()
	}
}

func (t *TopK) recompute() {
	defer func() {
		_ = recover()
	}()

	t.mu.Lock()
	defer t.mu.Unlock()

	t.recomputeLocked()
	t.lastRecompute = time.Now()
}

func (t *TopK) recomputeLocked() {
	t.routeTop = buildTop(t.routeCounts, t.routeK)
	t.poolTop = buildTop(t.poolCounts, t.poolK)
}

func buildTop(counts map[string]int64, limit int) map[string]struct{} {
	type pair struct {
		key   string
		count int64
	}
	items := make([]pair, 0, len(counts))
	for key, count := range counts {
		items = append(items, pair{key: key, count: count})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].key < items[j].key
		}
		return items[i].count > items[j].count
	})

	if limit > len(items) {
		limit = len(items)
	}
	result := make(map[string]struct{}, limit)
	for i := 0; i < limit; i++ {
		result[items[i].key] = struct{}{}
	}
	return result
}
