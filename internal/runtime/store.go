package runtime

import (
	"sync"
	"sync/atomic"
	"time"
)

const defaultMaxRetiredSnapshots = 10

type Store struct {
	current    atomic.Value
	mu         sync.Mutex
	retired    []*Snapshot
	maxRetired int
}

func NewStore(initial *Snapshot) *Store {
	store := &Store{maxRetired: defaultMaxRetiredSnapshots}
	store.current.Store(initial)
	return store
}

func (s *Store) Get() *Snapshot {
	if s == nil {
		return nil
	}
	value := s.current.Load()
	if value == nil {
		return nil
	}
	return value.(*Snapshot)
}

func (s *Store) Acquire() *Snapshot {
	snapshot := s.Get()
	if snapshot == nil {
		return nil
	}
	snapshot.IncRef()
	return snapshot
}

func (s *Store) Release(snapshot *Snapshot) {
	if snapshot == nil {
		return
	}
	snapshot.DecRef()
	if snapshot.Retired() && snapshot.RefCount() == 0 {
		s.Reap()
	}
}

func (s *Store) Swap(next *Snapshot) error {
	if s == nil {
		return nil
	}

	var previous *Snapshot
	value := s.current.Load()
	if value != nil {
		previous = value.(*Snapshot)
	}

	s.mu.Lock()
	if previous != nil {
		previous.MarkRetired(time.Now())
		s.retired = append(s.retired, previous)
	}
	s.current.Store(next)
	s.mu.Unlock()

	s.Reap()
	return nil
}

func (s *Store) RetiredCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	count := len(s.retired)
	s.mu.Unlock()
	return count
}

func (s *Store) Reap() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if len(s.retired) == 0 {
		s.mu.Unlock()
		return
	}
	retained := s.retired[:0]
	for _, snapshot := range s.retired {
		if snapshot == nil {
			continue
		}
		if snapshot.RefCount() != 0 {
			retained = append(retained, snapshot)
		}
	}
	s.retired = retained
	s.mu.Unlock()
}

func (s *Store) SetMaxRetired(limit int) {
	if s == nil {
		return
	}
	if limit <= 0 {
		limit = defaultMaxRetiredSnapshots
	}
	s.mu.Lock()
	s.maxRetired = limit
	s.mu.Unlock()
}

func (s *Store) maxRetiredSnapshots() int {
	if s == nil {
		return defaultMaxRetiredSnapshots
	}
	s.mu.Lock()
	limit := s.maxRetired
	s.mu.Unlock()
	if limit <= 0 {
		return defaultMaxRetiredSnapshots
	}
	return limit
}
