package cache

import (
	"errors"
	"sync"
	"time"
)

const DefaultMaxObjectBytes int64 = 50 * 1024 * 1024

type MemoryStore struct {
	mu             sync.RWMutex
	entries        map[string]Entry
	maxObjectBytes int64
}

func NewMemoryStore(maxObjectBytes int64) *MemoryStore {
	if maxObjectBytes <= 0 {
		maxObjectBytes = DefaultMaxObjectBytes
	}
	return &MemoryStore{
		entries:        make(map[string]Entry),
		maxObjectBytes: maxObjectBytes,
	}
}

func (m *MemoryStore) Get(key string) (Entry, bool) {
	if m == nil {
		return Entry{}, false
	}

	now := time.Now()
	m.mu.RLock()
	entry, ok := m.entries[key]
	m.mu.RUnlock()
	if !ok {
		return Entry{}, false
	}
	if !entry.ExpiresAt.IsZero() && now.After(entry.ExpiresAt) {
		m.Delete(key)
		return Entry{}, false
	}
	return entry, true
}

func (m *MemoryStore) Set(key string, entry Entry) error {
	if m == nil {
		return errors.New("cache store not initialized")
	}
	if m.maxObjectBytes > 0 && int64(len(entry.Body)) > m.maxObjectBytes {
		return errors.New("cache entry exceeds max object bytes")
	}
	m.mu.Lock()
	m.entries[key] = entry
	m.mu.Unlock()
	return nil
}

func (m *MemoryStore) Delete(key string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.entries, key)
	m.mu.Unlock()
}
