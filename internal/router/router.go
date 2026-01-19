package router

import (
	"net"
	"net/http"
	"strings"

	"modern_reverse_proxy/internal/policy"
)

type Router struct {
	routes []policy.Route
}

func NewRouter(routes []policy.Route) *Router {
	return &Router{routes: append([]policy.Route(nil), routes...)}
}

func (r *Router) Match(req *http.Request) (policy.Route, bool) {
	if r == nil {
		return policy.Route{}, false
	}
	host := req.Host
	if h, _, err := net.SplitHostPort(req.Host); err == nil {
		host = h
	}

	for _, route := range r.routes {
		if route.Host == host && strings.HasPrefix(req.URL.Path, route.PathPrefix) {
			if route.Methods != nil && !route.Methods[req.Method] {
				return policy.Route{}, false
			}
			return route, true
		}
	}

	return policy.Route{}, false
}
