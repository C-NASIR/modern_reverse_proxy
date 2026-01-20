package bundle

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type BundleMeta struct {
	Version   string `json:"version"`
	CreatedAt string `json:"created_at"`
	Source    string `json:"source"`
	Notes     string `json:"notes,omitempty"`
}

type Storage interface {
	Put(bundle Bundle) error
	Get(version string) (Bundle, bool)
	Latest() (Bundle, bool)
	List(limit int) []BundleMeta
}

type MemoryStorage struct {
	mu      sync.RWMutex
	bundles map[string]Bundle
	order   []string
	latest  string
}

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{bundles: make(map[string]Bundle)}
}

func (m *MemoryStorage) Put(bundle Bundle) error {
	if bundle.Meta.Version == "" {
		return errors.New("bundle version required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.bundles[bundle.Meta.Version]; !exists {
		m.order = append(m.order, bundle.Meta.Version)
	}
	m.bundles[bundle.Meta.Version] = bundle
	m.latest = bundle.Meta.Version
	return nil
}

func (m *MemoryStorage) Get(version string) (Bundle, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	bundle, ok := m.bundles[version]
	return bundle, ok
}

func (m *MemoryStorage) Latest() (Bundle, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.latest == "" {
		return Bundle{}, false
	}
	bundle, ok := m.bundles[m.latest]
	return bundle, ok
}

func (m *MemoryStorage) List(limit int) []BundleMeta {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if limit <= 0 {
		limit = 20
	}
	items := make([]BundleMeta, 0, limit)
	for i := len(m.order) - 1; i >= 0 && len(items) < limit; i-- {
		bundle, ok := m.bundles[m.order[i]]
		if !ok {
			continue
		}
		items = append(items, bundleMeta(bundle.Meta))
	}
	return items
}

type FileStorage struct {
	mu  sync.Mutex
	dir string
}

func NewFileStorage(dir string) (*FileStorage, error) {
	if dir == "" {
		return nil, errors.New("storage directory required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &FileStorage{dir: dir}, nil
}

func (f *FileStorage) Put(bundle Bundle) error {
	if bundle.Meta.Version == "" {
		return errors.New("bundle version required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	path := filepath.Join(f.dir, bundle.Meta.Version+".json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(f.dir, "latest"), []byte(bundle.Meta.Version), 0600)
}

func (f *FileStorage) Get(version string) (Bundle, bool) {
	path := filepath.Join(f.dir, version+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Bundle{}, false
	}
	var bundle Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return Bundle{}, false
	}
	return bundle, true
}

func (f *FileStorage) Latest() (Bundle, bool) {
	latest, err := os.ReadFile(filepath.Join(f.dir, "latest"))
	if err == nil {
		version := strings.TrimSpace(string(latest))
		if version != "" {
			if bundle, ok := f.Get(version); ok {
				return bundle, true
			}
		}
	}
	list := f.List(1)
	if len(list) == 0 {
		return Bundle{}, false
	}
	return f.Get(list[0].Version)
}

func (f *FileStorage) List(limit int) []BundleMeta {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return nil
	}
	items := make([]BundleMeta, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == "latest" || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(f.dir, name))
		if err != nil {
			continue
		}
		var bundle Bundle
		if err := json.Unmarshal(data, &bundle); err != nil {
			continue
		}
		items = append(items, bundleMeta(bundle.Meta))
	}
	sort.Slice(items, func(i, j int) bool {
		ti := parseMetaTime(items[i].CreatedAt)
		tj := parseMetaTime(items[j].CreatedAt)
		return ti.After(tj)
	})
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	return items[:limit]
}

func bundleMeta(meta Meta) BundleMeta {
	return BundleMeta{
		Version:   meta.Version,
		CreatedAt: meta.CreatedAt,
		Source:    meta.Source,
		Notes:     meta.Notes,
	}
}

func parseMetaTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
