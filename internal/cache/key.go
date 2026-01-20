package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"modern_reverse_proxy/internal/policy"
)

func BuildKey(req *http.Request, cachePolicy policy.CachePolicy) string {
	if req == nil {
		return ""
	}

	method := strings.ToUpper(req.Method)
	host := strings.ToLower(req.Host)
	path := req.URL.Path
	if path == "" {
		path = "/"
	}
	url := path
	if req.URL.RawQuery != "" {
		url += "?" + req.URL.RawQuery
	}

	var builder strings.Builder
	baseLen := len(method) + len(host) + len(url) + 12
	if len(cachePolicy.VaryHeaders) > 0 {
		baseLen += len(cachePolicy.VaryHeaders) * 16
	}
	if baseLen < 64 {
		baseLen = 64
	}
	builder.Grow(baseLen)
	builder.WriteString("m=")
	builder.WriteString(method)
	builder.WriteString("|h=")
	builder.WriteString(host)
	builder.WriteString("|u=")
	builder.WriteString(url)

	for _, header := range cachePolicy.VaryHeaders {
		name := strings.ToLower(strings.TrimSpace(header))
		if name == "" {
			continue
		}
		values := req.Header.Values(header)
		for i, value := range values {
			values[i] = strings.TrimSpace(value)
		}
		builder.WriteString("|v=")
		builder.WriteString(name)
		builder.WriteString(":")
		builder.WriteString(strings.Join(values, ","))
	}

	partition := cachePartition(req, cachePolicy)
	builder.WriteString("|p=")
	builder.WriteString(partition)

	return builder.String()
}

func cachePartition(req *http.Request, cachePolicy policy.CachePolicy) string {
	if cachePolicy.Public {
		return "public"
	}
	auth := ""
	if req != nil {
		auth = strings.TrimSpace(req.Header.Get("Authorization"))
	}
	if auth == "" {
		return "priv:anon"
	}
	hash := sha256.Sum256([]byte(auth))
	return "priv:" + hex.EncodeToString(hash[:16])
}
