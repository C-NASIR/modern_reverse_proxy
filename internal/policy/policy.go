package policy

import "time"

type Policy struct {
	RequestTimeout               time.Duration
	UpstreamDialTimeout          time.Duration
	UpstreamResponseHeaderTimeout time.Duration
}

type Route struct {
	ID         string
	Host       string
	PathPrefix string
	Methods    map[string]bool
	PoolName   string
	Policy     Policy
}
