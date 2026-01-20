package admin

import (
	"sync"
	"time"

	"modern_reverse_proxy/internal/config"
)

type Store struct {
	mu        sync.RWMutex
	version   string
	createdAt time.Time
	config    *config.Config
	raw       []byte
}

func NewStore() *Store {
	return &Store{}
}

func (s *Store) Set(version string, raw []byte, cfg *config.Config) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version = version
	s.createdAt = time.Now().UTC()
	s.config = cfg
	if raw != nil {
		copied := make([]byte, len(raw))
		copy(copied, raw)
		s.raw = copied
	}
}

func (s *Store) LatestVersion() (string, time.Time) {
	if s == nil {
		return "", time.Time{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version, s.createdAt
}
