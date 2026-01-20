package integration

import (
	"net/http"
	"testing"

	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/server"
	"modern_reverse_proxy/internal/traffic"
)

func startProxy(t *testing.T, cfgJSON string) (*server.Server, *runtime.Store, *registry.Registry, *traffic.Registry) {
	t.Helper()
	cfg, err := config.ParseJSON([]byte(cfgJSON))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}

	reg := registry.NewRegistry(0, 0)
	trafficReg := traffic.NewRegistry(0, 0)

	snap, err := runtime.BuildSnapshot(cfg, reg, nil, nil, trafficReg)
	if err != nil {
		reg.Close()
		trafficReg.Close()
		t.Fatalf("build snapshot: %v", err)
	}
	store := runtime.NewStore(snap)
	shutdownConfig, err := runtime.ShutdownFromConfig(cfg.Shutdown)
	if err != nil {
		reg.Close()
		trafficReg.Close()
		t.Fatalf("shutdown config: %v", err)
	}
	inflight := runtime.NewInflightTracker()

	proxyHandler := &proxy.Handler{Store: store, Registry: reg, Engine: proxy.NewEngine(reg, nil, nil, nil, nil), Inflight: inflight}
	mux := http.NewServeMux()
	mux.Handle("/", proxyHandler)

	serverHandle, err := server.StartServers(mux, nil, cfg.ListenAddr, snap.TLSAddr, server.Options{
		Limits:   snap.Limits,
		Shutdown: shutdownConfig,
		Inflight: inflight,
		Stoppers: []server.Stopper{reg, trafficReg},
	})
	if err != nil {
		reg.Close()
		trafficReg.Close()
		t.Fatalf("start proxy: %v", err)
	}

	t.Cleanup(func() {
		_ = serverHandle.Close()
		reg.Close()
		trafficReg.Close()
	})

	return serverHandle, store, reg, trafficReg
}
