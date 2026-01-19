package plugin

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

const defaultDialTimeout = 500 * time.Millisecond

type Registry struct {
	mu          sync.Mutex
	clients     map[string]*Client
	breakers    map[string]*Breaker
	dialTimeout time.Duration
}

func NewRegistry(dialTimeout time.Duration) *Registry {
	if dialTimeout <= 0 {
		dialTimeout = defaultDialTimeout
	}
	return &Registry{
		clients:     make(map[string]*Client),
		breakers:    make(map[string]*Breaker),
		dialTimeout: dialTimeout,
	}
}

func (r *Registry) GetClient(addr string) (*Client, error) {
	if r == nil {
		return nil, grpc.ErrClientConnClosing
	}
	r.mu.Lock()
	if client := r.clients[addr]; client != nil {
		r.mu.Unlock()
		return client, nil
	}
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), r.dialTimeout)
	defer cancel()
	conn, err := grpc.DialContext(
		ctx,
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		return nil, err
	}
	client := NewClient(conn)

	r.mu.Lock()
	if existing := r.clients[addr]; existing != nil {
		r.mu.Unlock()
		_ = client.Close()
		return existing, nil
	}
	r.clients[addr] = client
	r.mu.Unlock()
	return client, nil
}

func (r *Registry) GetBreaker(filterKey string, cfg BreakerConfig) *Breaker {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	breaker := r.breakers[filterKey]
	if breaker == nil {
		breaker = NewBreaker(cfg)
		r.breakers[filterKey] = breaker
		return breaker
	}
	breaker.UpdateConfig(cfg)
	return breaker
}

func (r *Registry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	clients := make([]*Client, 0, len(r.clients))
	for _, client := range r.clients {
		clients = append(clients, client)
	}
	r.clients = make(map[string]*Client)
	r.mu.Unlock()

	for _, client := range clients {
		_ = client.Close()
	}
}
