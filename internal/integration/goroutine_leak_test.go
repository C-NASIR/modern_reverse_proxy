package integration

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"modern_reverse_proxy/internal/breaker"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/outlier"
	"modern_reverse_proxy/internal/registry"
	runtimecore "modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/testutil"
	"modern_reverse_proxy/internal/traffic"
)

func TestGoroutineLeakAcrossSwaps(t *testing.T) {
	reg := registry.NewRegistry(0, 0)
	breakerReg := breaker.NewRegistry(0, 0)
	outlierReg := outlier.NewRegistry(0, 0, nil)
	trafficReg := traffic.NewRegistry(0, 0)
	defer reg.Close()
	defer breakerReg.Close()
	defer outlierReg.Close()
	defer trafficReg.Close()

	initialCfg, err := config.ParseJSON([]byte(leakTestConfig("127.0.0.1:30100")))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	initialSnap, err := runtimecore.BuildSnapshot(initialCfg, reg, breakerReg, outlierReg, trafficReg)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	store := runtimecore.NewStore(initialSnap)

	baseline := runtime.NumGoroutine()
	for swapIndex := 0; swapIndex < 20; swapIndex++ {
		addr := fmt.Sprintf("127.0.0.1:%d", 30200+swapIndex)
		cfg, err := config.ParseJSON([]byte(leakTestConfig(addr)))
		if err != nil {
			t.Fatalf("parse config: %v", err)
		}
		snap, err := runtimecore.BuildSnapshot(cfg, reg, breakerReg, outlierReg, trafficReg)
		if err != nil {
			t.Fatalf("build snapshot: %v", err)
		}
		if err := store.Swap(snap); err != nil {
			t.Fatalf("swap snapshot: %v", err)
		}
	}

	testutil.Eventually(t, 3*time.Second, 50*time.Millisecond, func() error {
		current := runtime.NumGoroutine()
		if current <= baseline+20 {
			return nil
		}
		return fmt.Errorf("goroutines=%d baseline=%d", current, baseline)
	})
}

func leakTestConfig(addr string) string {
	return fmt.Sprintf(`{
"listen_addr": "127.0.0.1:0",
"routes": [{"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1", "policy": {"traffic": {"enabled": true, "stable_pool": "p1", "canary_pool": "", "stable_weight": 100, "canary_weight": 0, "autodrain": {"enabled": true, "window_ms": 200, "min_requests": 1, "error_rate_multiplier": 1.0, "cooloff_ms": 200}}}}],
"pools": {"p1": {"endpoints": ["%s"], "health": {"interval_ms": 50, "timeout_ms": 50}, "outlier": {"enabled": true, "latency_enabled": true, "latency_window_size": 10, "latency_eval_interval_ms": 50, "latency_min_samples": 1, "latency_multiplier": 1}}}
}`, addr)
}
