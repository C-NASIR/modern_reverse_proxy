package retry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"

	"modern_reverse_proxy/internal/policy"
)

func IsIdempotentMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

func IsReplayableBody(req *http.Request) bool {
	if req == nil {
		return false
	}
	return req.Body == nil || req.ContentLength == 0
}

func ClassifyError(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return "dial", true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout", true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", true
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return "reset", true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "eof", true
	}
	return "", false
}

func ClassifyStatus(status int, retryPolicy policy.RetryPolicy) (string, bool) {
	if retryPolicy.RetryOnStatus == nil {
		return "", false
	}
	if !retryPolicy.RetryOnStatus[status] {
		return "", false
	}
	return fmt.Sprintf("status_%d", status), true
}
