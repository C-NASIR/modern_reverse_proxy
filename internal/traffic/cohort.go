package traffic

import (
	"errors"
	"net"
	"net/http"
	"strings"
)

type CohortExtractor struct {
	kind   cohortKind
	header string
}

type cohortKind int

const (
	cohortDisabled cohortKind = iota
	cohortIP
	cohortHeader
)

func NewCohortExtractor(spec string) (*CohortExtractor, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return nil, nil
	}
	if strings.EqualFold(trimmed, "ip") {
		return &CohortExtractor{kind: cohortIP}, nil
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "header:") {
		name := strings.TrimSpace(trimmed[len("header:"):])
		if name == "" {
			return nil, errors.New("cohort header key missing")
		}
		return &CohortExtractor{kind: cohortHeader, header: name}, nil
	}
	return nil, errors.New("unsupported cohort key")
}

func (c *CohortExtractor) Extract(r *http.Request) (string, bool) {
	if c == nil || r == nil {
		return "", false
	}
	switch c.kind {
	case cohortIP:
		value := strings.TrimSpace(r.RemoteAddr)
		if value == "" {
			return "", false
		}
		if host, _, err := net.SplitHostPort(value); err == nil {
			value = host
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return "", false
		}
		return value, true
	case cohortHeader:
		value := strings.TrimSpace(r.Header.Get(c.header))
		if value == "" {
			return "", false
		}
		return value, true
	default:
		return "", false
	}
}
