package integration

import (
	"net"
	"testing"
	"time"

	"modern_reverse_proxy/internal/testutil"
)

func TestSlowlorisHeaderTimeout(t *testing.T) {
	upstreamAddr, closeUpstream := testutil.StartUpstream(t, nil)
	defer closeUpstream()

	cfgJSON := buildProxyConfig(upstreamAddr, `"limits": {"read_header_timeout_ms": 100}`)
	serverHandle, _, _, _ := startProxy(t, cfgJSON)

	conn, err := net.Dial("tcp", serverHandle.HTTPAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: example.local\r\n"))
	if err != nil {
		t.Fatalf("write headers: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if _, err := conn.Write([]byte("\r\n")); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buffer := make([]byte, 1)
	if _, err := conn.Read(buffer); err == nil {
		t.Fatalf("expected connection to close after header timeout")
	}
}

func buildProxyConfig(upstreamAddr string, extra string) string {
	config := `{
"listen_addr": "127.0.0.1:0",
"routes": [{"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1"}],
"pools": {"p1": {"endpoints": ["` + upstreamAddr + `"]}}`
	if extra != "" {
		config += "," + extra
	}
	config += "}"
	return config
}
