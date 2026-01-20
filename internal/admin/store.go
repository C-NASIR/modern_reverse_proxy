package admin

import (
	"sync"

	"modern_reverse_proxy/internal/bundle"
)

const bundleHistoryLimit = 20

type Store struct {
	mu       sync.RWMutex
	current  string
	previous []string
	bundles  map[string]bundle.Bundle
}

func NewStore() *Store {
	return &Store{bundles: make(map[string]bundle.Bundle)}
}

func (s *Store) Record(entry bundle.Bundle) {
	if s == nil || entry.Meta.Version == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bundles == nil {
		s.bundles = make(map[string]bundle.Bundle)
	}
	if s.current != "" && s.current != entry.Meta.Version {
		s.previous = append([]string{s.current}, s.previous...)
		if len(s.previous) > bundleHistoryLimit {
			s.previous = s.previous[:bundleHistoryLimit]
		}
	}
	s.current = entry.Meta.Version
	s.bundles[entry.Meta.Version] = entry
}

func (s *Store) Get(version string) (bundle.Bundle, bool) {
	if s == nil {
		return bundle.Bundle{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.bundles[version]
	return item, ok
}

func (s *Store) Latest() (bundle.Bundle, bool) {
	if s == nil {
		return bundle.Bundle{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == "" {
		return bundle.Bundle{}, false
	}
	item, ok := s.bundles[s.current]
	return item, ok
}

func (s *Store) Previous() (bundle.Bundle, bool) {
	if s == nil {
		return bundle.Bundle{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.previous) == 0 {
		return bundle.Bundle{}, false
	}
	version := s.previous[0]
	item, ok := s.bundles[version]
	return item, ok
}

func (s *Store) CurrentVersion() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}
