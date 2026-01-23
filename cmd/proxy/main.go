package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"modern_reverse_proxy/internal/admin"
	"modern_reverse_proxy/internal/apply"
	"modern_reverse_proxy/internal/breaker"
	"modern_reverse_proxy/internal/bundle"
	"modern_reverse_proxy/internal/cache"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/limits"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/outlier"
	"modern_reverse_proxy/internal/plugin"
	"modern_reverse_proxy/internal/provider"
	"modern_reverse_proxy/internal/proxy"
	"modern_reverse_proxy/internal/pull"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/rollout"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/server"
	"modern_reverse_proxy/internal/traffic"
)

func main() {
	configFile := flag.String("config-file", "", "Path to JSON config")
	httpAddr := flag.String("http-addr", ":8080", "HTTP listen address")
	tlsAddr := flag.String("tls-addr", "", "TLS listen address (empty disables TLS)")
	adminAddr := flag.String("admin-addr", ":9000", "Admin listen address")
	enableAdmin := flag.Bool("enable-admin", true, "Enable admin listener")
	enablePull := flag.Bool("enable-pull", false, "Enable pull mode")
	pullURL := flag.String("pull-url", "", "Pull mode base URL")
	pullIntervalMS := flag.Int("pull-interval-ms", 5000, "Pull mode interval in ms")
	publicKeyFile := flag.String("public-key-file", "", "Public key file for signed bundles")
	adminToken := flag.String("admin-token", "", "Admin API token")
	logJSON := flag.Bool("log-json", true, "Emit JSON logs")
	flag.Parse()

	configureLogging(*logJSON)

	cfg, err := loadConfig(*configFile)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	reg := registry.NewRegistry(0, 0)
	retryReg := registry.NewRetryRegistry(0, 0)
	metrics := obs.NewMetrics(obs.MetricsConfig{})
	obs.SetDefaultMetrics(metrics)
	breakerReg := breaker.NewRegistry(0, 0)
	outlierReg := outlier.NewRegistry(0, 0, metrics.RecordOutlierEjection)
	trafficReg := traffic.NewRegistry(0, 0)
	pluginReg := plugin.NewRegistry(0)

	publicKey, err := loadPublicKey(*publicKeyFile)
	if err != nil {
		log.Fatalf("public key: %v", err)
	}

	snap, err := runtime.BuildSnapshot(cfg, reg, breakerReg, outlierReg, trafficReg)
	if err != nil {
		log.Fatalf("build snapshot: %v", err)
	}

	store := runtime.NewStore(snap)
	shutdownConfig, err := runtime.ShutdownFromConfig(cfg.Shutdown)
	if err != nil {
		log.Fatalf("shutdown config: %v", err)
	}
	inflight := runtime.NewInflightTracker()
	cacheStore := cache.NewMemoryStore(cache.DefaultMaxObjectBytes)
	cacheCoalescer := cache.NewCoalescer(cache.DefaultMaxFlights)
	cacheLayer := cache.NewCache(cacheStore, cacheCoalescer)
	adminProvider := provider.NewAdminPush()
	providers := []provider.Provider{adminProvider}
	if *configFile != "" {
		providers = append(providers, provider.NewFileProvider(*configFile))
	}
	applyManager := apply.NewManager(apply.ManagerConfig{
		Store:           store,
		Registry:        reg,
		BreakerRegistry: breakerReg,
		OutlierRegistry: outlierReg,
		TrafficRegistry: trafficReg,
		Providers:       providers,
		AdminProvider:   adminProvider,
		Pressure:        runtime.NewPressure(store),
	})
	rolloutManager := rollout.NewManager(rollout.Config{
		ApplyManager:     applyManager,
		Store:            store,
		Metrics:          metrics,
		LockedBake:       parseDurationMS(os.Getenv("ROLLOUT_LOCKED_BAKE_MS"), time.Minute),
		ErrorRateWindow:  parseDurationMS(os.Getenv("ROLLOUT_ERROR_WINDOW_MS"), 10*time.Second),
		ErrorRatePercent: parseFloatEnv(os.Getenv("ROLLOUT_ERROR_PERCENT"), 1),
	})
	engine := proxy.NewEngine(reg, retryReg, metrics, breakerReg, outlierReg)
	handler := &proxy.Handler{
		Store:           store,
		Registry:        reg,
		RetryRegistry:   retryReg,
		BreakerRegistry: breakerReg,
		OutlierRegistry: outlierReg,
		PluginRegistry:  pluginReg,
		Engine:          engine,
		Metrics:         metrics,
		Cache:           cacheLayer,
		Inflight:        inflight,
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Handler())
	mux.Handle("/", handler)

	var tlsBaseConfig *tls.Config
	if snap.TLSEnabled {
		tlsBaseConfig = server.BaseTLSConfig(store)
	}

	stoppers := []server.Stopper{reg, retryReg, breakerReg, outlierReg, trafficReg, pluginReg}
	if *enablePull {
		if *pullURL == "" {
			log.Fatalf("pull-url is required when pull mode is enabled")
		}
		if len(publicKey) == 0 {
			log.Fatalf("public-key-file is required for pull mode")
		}
		puller := pull.NewPuller(pull.Config{
			Enabled:        true,
			BaseURL:        *pullURL,
			Interval:       time.Duration(*pullIntervalMS) * time.Millisecond,
			Jitter:         parseDurationMS(os.Getenv("PULL_JITTER_MS"), 500*time.Millisecond),
			PublicKey:      publicKey,
			RolloutManager: rolloutManager,
			Store:          store,
			Token:          os.Getenv("PULL_TOKEN"),
		})
		pullCtx, pullCancel := context.WithCancel(context.Background())
		stoppers = append(stoppers, server.StopFunc(func(ctx context.Context) error {
			pullCancel()
			return nil
		}))
		go puller.Run(pullCtx)
	}

	serverHandle, err := server.StartServers(mux, tlsBaseConfig, *httpAddr, *tlsAddr, server.Options{
		Limits:   snap.Limits,
		Shutdown: shutdownConfig,
		Inflight: inflight,
		Stoppers: stoppers,
		CloseIdle: []func(){
			engine.CloseIdleConnections,
		},
	})
	if err != nil {
		log.Fatalf("start servers: %v", err)
	}
	if serverHandle.HTTPAddr != "" {
		log.Printf("listening on http://%s", serverHandle.HTTPAddr)
	}
	if serverHandle.TLSAddr != "" {
		log.Printf("listening on https://%s", serverHandle.TLSAddr)
	}

	if err := startAdmin(*enableAdmin, *adminAddr, *adminToken, store, applyManager, publicKey, rolloutManager); err != nil {
		log.Fatalf("admin: %v", err)
	}

	select {}
}

func loadPublicKey(path string) (ed25519.PublicKey, error) {
	if path == "" {
		path = os.Getenv("PUBLIC_KEY_FILE")
	}
	if path == "" {
		return nil, nil
	}
	return bundle.LoadPublicKey(path)
}

func loadConfig(path string) (*config.Config, error) {
	if path == "" {
		return &config.Config{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return config.ParseJSON(data)
}

func startAdmin(enabled bool, addr string, token string, store *runtime.Store, applyManager *apply.Manager, publicKey ed25519.PublicKey, rolloutManager *rollout.Manager) error {
	if !enabled {
		return nil
	}
	if addr == "" {
		return errors.New("admin-addr is required when admin is enabled")
	}
	adminToken := token
	if adminToken == "" {
		adminToken = os.Getenv("ADMIN_TOKEN")
	}
	adminCert := envOrFallback("ADMIN_TLS_CERT_FILE", "ADMIN_CERT_FILE")
	adminKey := envOrFallback("ADMIN_TLS_KEY_FILE", "ADMIN_KEY_FILE")
	adminCA := strings.TrimSpace(os.Getenv("ADMIN_CLIENT_CA_FILE"))
	allowUnsigned := os.Getenv("ALLOW_UNSIGNED_ADMIN_CONFIG") == "true"
	if adminToken == "" {
		return errors.New("ADMIN_TOKEN is required for admin listener")
	}
	if adminCert == "" || adminKey == "" {
		return errors.New("ADMIN_TLS_CERT_FILE and ADMIN_TLS_KEY_FILE are required for admin listener")
	}
	if adminCA == "" {
		return errors.New("ADMIN_CLIENT_CA_FILE is required for admin listener")
	}
	auth, err := admin.NewAuthenticator(admin.AuthConfig{Token: adminToken, ClientCAFile: adminCA})
	if err != nil {
		return err
	}
	adminTLS, err := admin.TLSConfig(adminCert, adminKey, adminCA)
	if err != nil {
		return err
	}
	adminHandler := admin.NewHandler(admin.HandlerConfig{
		Store:          store,
		ApplyManager:   applyManager,
		Auth:           auth,
		RateLimiter:    admin.NewRateLimiter(admin.RateLimitConfig{}),
		AdminStore:     admin.NewStore(),
		PublicKey:      publicKey,
		AllowUnsigned:  allowUnsigned,
		RolloutManager: rolloutManager,
	})
	adminServer, err := server.StartServers(adminHandler, adminTLS, "", addr, server.Options{
		Limits:   limits.Default(),
		Shutdown: runtime.DefaultShutdownConfig(),
	})
	if err != nil {
		return err
	}
	if adminServer.TLSAddr != "" {
		log.Printf("admin listening on https://%s", adminServer.TLSAddr)
	}
	return nil
}

func envOrFallback(primary string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(primary))
	if value == "" {
		value = strings.TrimSpace(os.Getenv(fallback))
	}
	return value
}

func configureLogging(jsonEnabled bool) {
	if !jsonEnabled {
		log.SetFlags(log.LstdFlags)
		return
	}
	log.SetFlags(0)
	log.SetOutput(&jsonLogWriter{writer: os.Stdout})
}

type jsonLogWriter struct {
	writer io.Writer
}

func (j *jsonLogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	if msg == "" {
		return len(p), nil
	}
	entry := map[string]string{
		"ts":  time.Now().UTC().Format(time.RFC3339Nano),
		"msg": msg,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		_, writeErr := j.writer.Write(p)
		if writeErr != nil {
			return len(p), writeErr
		}
		return len(p), err
	}
	data = append(data, '\n')
	_, writeErr := j.writer.Write(data)
	if writeErr != nil {
		return len(p), writeErr
	}
	return len(p), nil
}

func parseDurationMS(value string, fallback time.Duration) time.Duration {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return time.Duration(parsed) * time.Millisecond
}

func parseFloatEnv(value string, fallback float64) float64 {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}
