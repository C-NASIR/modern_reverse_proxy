package distributor

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

const tokenHeader = "X-Distributor-Token"

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.mux == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if s.requireToken && s.token != "" {
		if r.Header.Get(tokenHeader) != s.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleLatest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.storage == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	bundle, ok := s.storage.Latest()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	writeJSON(w, bundle)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.storage == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	version := strings.TrimPrefix(r.URL.Path, "/bundles/")
	if version == "" || strings.Contains(version, "/") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	bundle, ok := s.storage.Get(version)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	writeJSON(w, bundle)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.storage == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	limit := 20
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	meta := s.storage.List(limit)
	writeJSON(w, meta)
}

func writeJSON(w http.ResponseWriter, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
