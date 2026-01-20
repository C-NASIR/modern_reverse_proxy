package runtime

import (
	"fmt"
	"time"

	"modern_reverse_proxy/internal/config"
)

const (
	defaultDrain           = 2 * time.Second
	defaultGracefulTimeout = 5 * time.Second
	defaultForceClose      = 2 * time.Second
)

type ShutdownConfig struct {
	Drain           time.Duration
	GracefulTimeout time.Duration
	ForceClose      time.Duration
}

func ShutdownFromConfig(cfg config.ShutdownConfig) (ShutdownConfig, error) {
	shutdown := DefaultShutdownConfig()
	if cfg.DrainMS > 0 {
		shutdown.Drain = time.Duration(cfg.DrainMS) * time.Millisecond
	} else if cfg.DrainMS < 0 {
		return ShutdownConfig{}, fmt.Errorf("drain_ms must be non-negative")
	}
	if cfg.GracefulTimeoutMS > 0 {
		shutdown.GracefulTimeout = time.Duration(cfg.GracefulTimeoutMS) * time.Millisecond
	} else if cfg.GracefulTimeoutMS < 0 {
		return ShutdownConfig{}, fmt.Errorf("graceful_timeout_ms must be non-negative")
	}
	if cfg.ForceCloseMS > 0 {
		shutdown.ForceClose = time.Duration(cfg.ForceCloseMS) * time.Millisecond
	} else if cfg.ForceCloseMS < 0 {
		return ShutdownConfig{}, fmt.Errorf("force_close_ms must be non-negative")
	}
	return shutdown, nil
}

func DefaultShutdownConfig() ShutdownConfig {
	return ShutdownConfig{
		Drain:           defaultDrain,
		GracefulTimeout: defaultGracefulTimeout,
		ForceClose:      defaultForceClose,
	}
}

func ApplyShutdownDefaults(cfg ShutdownConfig) ShutdownConfig {
	defaults := DefaultShutdownConfig()
	if cfg.Drain <= 0 {
		cfg.Drain = defaults.Drain
	}
	if cfg.GracefulTimeout <= 0 {
		cfg.GracefulTimeout = defaults.GracefulTimeout
	}
	if cfg.ForceClose <= 0 {
		cfg.ForceClose = defaults.ForceClose
	}
	return cfg
}
