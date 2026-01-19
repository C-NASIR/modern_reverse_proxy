package main

import (
	"crypto/tls"
	"log"
	"net/http"
	"os"

	"modern_reverse_proxy/internal/breaker"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/outlier"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/server"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("usage: %s <config.json>", os.Args[0])
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		log.Fatalf("read config: %v", err)
	}
	cfg, err := config.ParseJSON(data)
	if err != nil {
		log.Fatalf("parse config: %v", err)
	}
	reg := registry.NewRegistry(0, 0)
	retryReg := registry.NewRetryRegistry(0, 0)
	metrics := obs.NewMetrics(obs.MetricsConfig{})
	obs.SetDefaultMetrics(metrics)
	breakerReg := breaker.NewRegistry(0, 0)
	outlierReg := outlier.NewRegistry(0, 0, metrics.RecordOutlierEjection)

	snap, err := runtime.BuildSnapshot(cfg, reg, breakerReg, outlierReg)
	if err != nil {
		log.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(snap)
	handler := &proxy.Handler{
		Store:           store,
		Registry:        reg,
		RetryRegistry:   retryReg,
		BreakerRegistry: breakerReg,
		OutlierRegistry: outlierReg,
		Engine:          proxy.NewEngine(reg, retryReg, metrics, breakerReg, outlierReg),
		Metrics:         metrics,
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Handler())
	mux.Handle("/", handler)

	listenAddr := cfg.ListenAddr
	if listenAddr == "" {
		listenAddr = "127.0.0.1:8080"
	}

	var tlsBaseConfig *tls.Config
	if snap.TLSEnabled {
		tlsBaseConfig = server.BaseTLSConfig(store)
	}

	serverHandle, err := server.StartServers(mux, tlsBaseConfig, listenAddr, snap.TLSAddr)
	if err != nil {
		log.Fatalf("start servers: %v", err)
	}
	if serverHandle.HTTPAddr != "" {
		log.Printf("listening on http://%s", serverHandle.HTTPAddr)
	}
	if serverHandle.TLSAddr != "" {
		log.Printf("listening on https://%s", serverHandle.TLSAddr)
	}
	select {}
}
