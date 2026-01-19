package config

import (
	"encoding/json"
)

type Config struct {
	ListenAddr string          `json:"listen_addr"`
	Routes     []Route         `json:"routes"`
	Pools      map[string]Pool `json:"pools"`
}

type Route struct {
	ID         string `json:"id"`
	Host       string `json:"host"`
	PathPrefix string `json:"path_prefix"`
	Methods    []string `json:"methods"`
	Pool       string `json:"pool"`
	Policy     RoutePolicy `json:"policy"`
}

type Pool struct {
	Endpoints []string `json:"endpoints"`
}

type RoutePolicy struct {
	RequestTimeoutMS               int `json:"request_timeout_ms"`
	UpstreamDialTimeoutMS          int `json:"upstream_dial_timeout_ms"`
	UpstreamResponseHeaderTimeoutMS int `json:"upstream_response_header_timeout_ms"`
}

func ParseJSON(data []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
