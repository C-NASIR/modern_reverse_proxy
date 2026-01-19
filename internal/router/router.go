package router

import (
	"net"
	"net/http"
	"strings"
)

type RouteConfig struct {
	ID         string
	Host       string
	PathPrefix string
	Pool       string
}

type Route struct {
	ID         string
	Host       string
	PathPrefix string
	PoolName   string
}

type Router struct {
	routes []Route
}

func NewRouter(configs []RouteConfig) *Router {
	routes := make([]Route, 0, len(configs))
	for _, cfg := range configs {
		routes = append(routes, Route{
			ID:         cfg.ID,
			Host:       cfg.Host,
			PathPrefix: cfg.PathPrefix,
			PoolName:   cfg.Pool,
		})
	}
	return &Router{routes: routes}
}

func (r *Router) Match(req *http.Request) (Route, bool) {
	if r == nil {
		return Route{}, false
	}
	host := req.Host
	if h, _, err := net.SplitHostPort(req.Host); err == nil {
		host = h
	}

	for _, route := range r.routes {
		if route.Host == host && strings.HasPrefix(req.URL.Path, route.PathPrefix) {
			return route, true
		}
	}

	return Route{}, false
}
