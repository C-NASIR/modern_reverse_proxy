package provider

import (
	"context"

	"modern_reverse_proxy/internal/config"
)

type Provider interface {
	Name() string
	Priority() int
	Load(ctx context.Context) (*config.Config, error)
}
