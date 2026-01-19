package runtime

import (
	"sync/atomic"

	"modern_reverse_proxy/internal/router"
)

type Pool interface {
	Pick() string
}

type Snapshot struct {
	Router *router.Router
	Pools  map[string]Pool
}

type Store struct {
	v atomic.Value
}

func NewStore(initial *Snapshot) *Store {
	store := &Store{}
	store.v.Store(initial)
	return store
}

func (s *Store) Get() *Snapshot {
	if s == nil {
		return nil
	}
	value := s.v.Load()
	if value == nil {
		return nil
	}
	return value.(*Snapshot)
}

func (s *Store) Swap(next *Snapshot) {
	if s == nil {
		return
	}
	s.v.Store(next)
}
