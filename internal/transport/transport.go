package transport

import (
	"net"
	"net/http"
	"time"
)

const (
	defaultDialTimeout           = time.Second
	defaultTLSHandshakeTimeout   = 5 * time.Second
	defaultResponseHeaderTimeout = 5 * time.Second
	defaultExpectContinueTimeout = time.Second
	defaultIdleConnTimeout       = 90 * time.Second
	defaultMaxIdleConns          = 10000
	defaultMaxIdleConnsPerHost   = 256
)

type Options struct {
	DialTimeout           time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
	ExpectContinueTimeout time.Duration
	IdleConnTimeout       time.Duration
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	MaxConnsPerHost       int
}

func DefaultOptions() Options {
	return Options{
		DialTimeout:           defaultDialTimeout,
		TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
		ResponseHeaderTimeout: defaultResponseHeaderTimeout,
		ExpectContinueTimeout: defaultExpectContinueTimeout,
		IdleConnTimeout:       defaultIdleConnTimeout,
		MaxIdleConns:          defaultMaxIdleConns,
		MaxIdleConnsPerHost:   defaultMaxIdleConnsPerHost,
	}
}

func NewTransport(opts Options) *http.Transport {
	opts = normalizeOptions(opts)

	dialer := &net.Dialer{Timeout: opts.DialTimeout}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   opts.TLSHandshakeTimeout,
		ResponseHeaderTimeout: opts.ResponseHeaderTimeout,
		ExpectContinueTimeout: opts.ExpectContinueTimeout,
		IdleConnTimeout:       opts.IdleConnTimeout,
		MaxIdleConns:          opts.MaxIdleConns,
		MaxIdleConnsPerHost:   opts.MaxIdleConnsPerHost,
		MaxConnsPerHost:       opts.MaxConnsPerHost,
		ForceAttemptHTTP2:     true,
	}
}

func normalizeOptions(opts Options) Options {
	defaults := DefaultOptions()
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = defaults.DialTimeout
	}
	if opts.TLSHandshakeTimeout <= 0 {
		opts.TLSHandshakeTimeout = defaults.TLSHandshakeTimeout
	}
	if opts.ResponseHeaderTimeout <= 0 {
		opts.ResponseHeaderTimeout = defaults.ResponseHeaderTimeout
	}
	if opts.ExpectContinueTimeout <= 0 {
		opts.ExpectContinueTimeout = defaults.ExpectContinueTimeout
	}
	if opts.IdleConnTimeout <= 0 {
		opts.IdleConnTimeout = defaults.IdleConnTimeout
	}
	if opts.MaxIdleConns <= 0 {
		opts.MaxIdleConns = defaults.MaxIdleConns
	}
	if opts.MaxIdleConnsPerHost <= 0 {
		opts.MaxIdleConnsPerHost = defaults.MaxIdleConnsPerHost
	}
	if opts.MaxConnsPerHost < 0 {
		opts.MaxConnsPerHost = 0
	}
	return opts
}
