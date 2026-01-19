package testutil

import (
	"testing"
	"time"
)

func Eventually(t *testing.T, timeout time.Duration, interval time.Duration, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		if err := fn(); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(interval)
	}

	if lastErr != nil {
		t.Fatalf("condition not met: %v", lastErr)
	}
	t.Fatalf("condition not met before timeout")
}
