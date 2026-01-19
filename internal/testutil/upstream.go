package testutil

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func StartUpstream(t *testing.T, handler http.Handler) (string, func()) {
	t.Helper()
	if handler == nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	}

	server := httptest.NewServer(handler)
	return server.Listener.Addr().String(), server.Close
}
