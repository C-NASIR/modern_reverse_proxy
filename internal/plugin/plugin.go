package plugin

import (
	"net/http"
	"strings"
	"time"
)

type FailureMode string

const (
	FailureModeFailOpen  FailureMode = "fail_open"
	FailureModeFailClose FailureMode = "fail_closed"
)

type BreakerConfig struct {
	Enabled             bool
	ConsecutiveFailures int
	OpenDuration        time.Duration
	HalfOpenProbes      int
}

type Filter struct {
	Name            string
	Addr            string
	RequestTimeout  time.Duration
	ResponseTimeout time.Duration
	FailureMode     FailureMode
	Breaker         BreakerConfig
}

type Policy struct {
	Enabled bool
	Filters []Filter
}

func FilterKey(name string, addr string) string {
	return name + "@" + addr
}

func HeadersToMap(header http.Header) map[string]string {
	if len(header) == 0 {
		return nil
	}
	result := make(map[string]string, len(header))
	for key, values := range header {
		if len(values) == 0 {
			continue
		}
		result[key] = strings.Join(values, ",")
	}
	return result
}

func ApplyHeaderMutations(target http.Header, mutations map[string]string) bool {
	if len(mutations) == 0 {
		return false
	}
	denied := false
	for key, value := range mutations {
		canon := http.CanonicalHeaderKey(strings.TrimSpace(key))
		if canon == "" {
			continue
		}
		if isHopByHopHeader(canon) {
			denied = true
			continue
		}
		target.Set(canon, value)
	}
	return denied
}

func isHopByHopHeader(header string) bool {
	switch header {
	case "Connection", "Keep-Alive", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}
