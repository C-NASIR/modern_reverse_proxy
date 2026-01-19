package cache

type Cache struct {
	Store     Store
	Coalescer *Coalescer
}

func NewCache(store Store, coalescer *Coalescer) *Cache {
	return &Cache{Store: store, Coalescer: coalescer}
}
