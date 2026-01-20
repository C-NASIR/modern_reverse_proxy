package transport

import "net/http"

func safeCloseIdle(transport *http.Transport) {
	if transport == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	transport.CloseIdleConnections()
}
