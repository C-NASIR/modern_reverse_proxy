package distributor

import (
	"net/http"

	"modern_reverse_proxy/internal/bundle"
)

type Config struct {
	Storage      bundle.Storage
	Token        string
	RequireToken bool
}

type Server struct {
	storage      bundle.Storage
	token        string
	requireToken bool
	mux          *http.ServeMux
}

func NewHandler(cfg Config) *Server {
	server := &Server{
		storage:      cfg.Storage,
		token:        cfg.Token,
		requireToken: cfg.RequireToken,
		mux:          http.NewServeMux(),
	}
	server.mux.HandleFunc("/bundles/latest", server.handleLatest)
	server.mux.HandleFunc("/bundles", server.handleList)
	server.mux.HandleFunc("/bundles/", server.handleGet)
	return server
}
