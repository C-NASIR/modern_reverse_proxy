package limits

import (
	"fmt"
	"time"

	"modern_reverse_proxy/internal/config"
)

const (
	defaultMaxHeaderBytes    = 64 * 1024
	defaultMaxHeaderCount    = 200
	defaultMaxURLBytes       = 8 * 1024
	defaultMaxBodyBytes      = 10 * 1024 * 1024
	defaultReadHeaderTimeout = 2 * time.Second
	defaultIdleTimeout       = 30 * time.Second
)

type Limits struct {
	MaxHeaderBytes        int
	MaxHeaderCount        int
	MaxURLBytes           int
	MaxBodyBytes          int64
	ReadHeaderTimeout     time.Duration
	ReadTimeout           time.Duration
	WriteTimeout          time.Duration
	IdleTimeout           time.Duration
	ResponseStreamTimeout time.Duration
}

func Default() Limits {
	return Limits{
		MaxHeaderBytes:        defaultMaxHeaderBytes,
		MaxHeaderCount:        defaultMaxHeaderCount,
		MaxURLBytes:           defaultMaxURLBytes,
		MaxBodyBytes:          defaultMaxBodyBytes,
		ReadHeaderTimeout:     defaultReadHeaderTimeout,
		ReadTimeout:           0,
		WriteTimeout:          0,
		IdleTimeout:           defaultIdleTimeout,
		ResponseStreamTimeout: 0,
	}
}

func FromConfig(cfg config.LimitsConfig) (Limits, error) {
	limits := Default()
	if cfg.MaxHeaderBytes > 0 {
		limits.MaxHeaderBytes = cfg.MaxHeaderBytes
	}
	if cfg.MaxHeaderCount > 0 {
		limits.MaxHeaderCount = cfg.MaxHeaderCount
	}
	if cfg.MaxURLBytes > 0 {
		limits.MaxURLBytes = cfg.MaxURLBytes
	}
	if cfg.MaxBodyBytes != nil {
		limits.MaxBodyBytes = *cfg.MaxBodyBytes
	}
	if cfg.ReadHeaderTimeoutMS > 0 {
		limits.ReadHeaderTimeout = time.Duration(cfg.ReadHeaderTimeoutMS) * time.Millisecond
	} else if cfg.ReadHeaderTimeoutMS < 0 {
		return Limits{}, fmt.Errorf("read_header_timeout_ms must be positive")
	}
	limits.ReadTimeout = durationOrZero(cfg.ReadTimeoutMS)
	limits.WriteTimeout = durationOrZero(cfg.WriteTimeoutMS)
	if cfg.IdleTimeoutMS > 0 {
		limits.IdleTimeout = time.Duration(cfg.IdleTimeoutMS) * time.Millisecond
	}
	limits.ResponseStreamTimeout = durationOrZero(cfg.ResponseStreamTimeoutMS)

	if limits.MaxHeaderBytes <= 0 {
		return Limits{}, fmt.Errorf("max_header_bytes must be positive")
	}
	if limits.MaxHeaderCount <= 0 {
		return Limits{}, fmt.Errorf("max_header_count must be positive")
	}
	if limits.MaxURLBytes <= 0 {
		return Limits{}, fmt.Errorf("max_url_bytes must be positive")
	}
	if limits.MaxBodyBytes < 0 {
		return Limits{}, fmt.Errorf("max_body_bytes must be non-negative")
	}
	if limits.ReadHeaderTimeout <= 0 {
		return Limits{}, fmt.Errorf("read_header_timeout_ms must be positive")
	}
	return limits, nil
}

func durationOrZero(milliseconds int) time.Duration {
	if milliseconds <= 0 {
		return 0
	}
	return time.Duration(milliseconds) * time.Millisecond
}
