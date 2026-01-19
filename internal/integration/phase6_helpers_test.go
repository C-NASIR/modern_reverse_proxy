package integration

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func fetchMetrics(t *testing.T, server *httptest.Server) string {
	t.Helper()
	resp, err := server.Client().Get(server.URL + "/metrics")
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	return string(body)
}

func metricValue(text string, metric string, labels map[string]string) (float64, bool) {
	lines := strings.Split(text, "\n")
	total := 0.0
	found := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, metric+"{") {
			continue
		}
		match := true
		for key, value := range labels {
			if !strings.Contains(line, key+"=\""+value+"\"") {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		value, err := parseFloat(parts[len(parts)-1])
		if err != nil {
			return 0, false
		}
		found = true
		total += value
	}
	return total, found
}
