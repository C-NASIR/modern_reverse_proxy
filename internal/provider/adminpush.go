package provider

import (
	"context"
	"sync"

	"modern_reverse_proxy/internal/config"
)

const AdminPriority = 100

type AdminPush struct {
	mu  sync.RWMutex
	cfg *config.Config
}

func NewAdminPush() *AdminPush {
	return &AdminPush{}
}

func (p *AdminPush) Name() string {
	return "admin"
}

func (p *AdminPush) Priority() int {
	return AdminPriority
}

func (p *AdminPush) Load(ctx context.Context) (*config.Config, error) {
	_ = ctx
	if p == nil {
		return nil, nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.cfg == nil {
		return nil, nil
	}
	return p.cfg, nil
}

func (p *AdminPush) Swap(cfg *config.Config) *config.Config {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	prev := p.cfg
	p.cfg = cfg
	return prev
}
