package integration

import (
	"testing"

	"modern_reverse_proxy/internal/obs"
)

func TestSensitiveHeaderRedaction(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "Authorization", value: "Bearer secret"},
		{name: "cookie", value: "a=b"},
		{name: "Set-Cookie", value: "a=b"},
		{name: "X-Api-Key", value: "token"},
		{name: "Proxy-Authorization", value: "Basic abc"},
	}

	for _, test := range tests {
		if got := obs.RedactHeaderValue(test.name, test.value); got != "[redacted]" {
			t.Fatalf("expected %s to be redacted", test.name)
		}
	}

	if got := obs.RedactHeaderValue("X-Request-ID", "trace"); got != "trace" {
		t.Fatalf("expected non-sensitive header to remain")
	}
}
