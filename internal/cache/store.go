package cache

import (
	"net/http"
	"time"
)

type Entry struct {
	Status    int
	Header    http.Header
	Body      []byte
	ExpiresAt time.Time
	StoredAt  time.Time
}

type Store interface {
	Get(key string) (Entry, bool)
	Set(key string, entry Entry) error
	Delete(key string)
}
