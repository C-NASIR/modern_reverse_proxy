package cache

import (
	"sync"
	"time"
)

const DefaultMaxFlights = 10000

type Flight struct {
	done      chan struct{}
	result    Entry
	ok        bool
	err       error
	startedAt time.Time
}

type Coalescer struct {
	mu         sync.Mutex
	flights    map[string]*Flight
	maxFlights int
}

func NewCoalescer(maxFlights int) *Coalescer {
	if maxFlights <= 0 {
		maxFlights = DefaultMaxFlights
	}
	return &Coalescer{flights: make(map[string]*Flight), maxFlights: maxFlights}
}

func (c *Coalescer) Start(key string) (*Flight, bool, bool) {
	if c == nil {
		return nil, false, false
	}
	if key == "" {
		return nil, false, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.flights[key]; ok {
		return existing, false, true
	}
	if c.maxFlights > 0 && len(c.flights) >= c.maxFlights {
		return nil, false, false
	}
	flight := &Flight{done: make(chan struct{}), startedAt: time.Now()}
	c.flights[key] = flight
	return flight, true, true
}

func (c *Coalescer) Finish(key string, flight *Flight, entry Entry, ok bool, err error) {
	if c == nil || flight == nil {
		return
	}
	c.mu.Lock()
	if current, exists := c.flights[key]; exists && current == flight {
		delete(c.flights, key)
	}
	c.mu.Unlock()
	flight.result = entry
	flight.ok = ok
	flight.err = err
	close(flight.done)
}

func (c *Coalescer) Wait(flight *Flight, timeout time.Duration) (Entry, bool, error, bool) {
	if flight == nil {
		return Entry{}, false, nil, false
	}
	if timeout <= 0 {
		return Entry{}, false, nil, false
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-flight.done:
		return flight.result, flight.ok, flight.err, true
	case <-timer.C:
		return Entry{}, false, nil, false
	}
}
