package apply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"modern_reverse_proxy/internal/breaker"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/outlier"
	"modern_reverse_proxy/internal/provider"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/traffic"
)

const DefaultMaxConfigBytes = 10 * 1024 * 1024
const DefaultCompileTimeout = 2 * time.Second

type Mode int

const (
	ModeApply Mode = iota
	ModeValidate
)

var (
	ErrConfigTooLarge = errors.New("config too large")
	ErrCompileTimeout = errors.New("compile timeout")
	ErrPressure       = errors.New("config_pressure")
)

type PressureChecker interface {
	UnderPressure() bool
}

type ManagerConfig struct {
	Store           *runtime.Store
	Registry        *registry.Registry
	BreakerRegistry *breaker.Registry
	OutlierRegistry *outlier.Registry
	TrafficRegistry *traffic.Registry
	Providers       []provider.Provider
	AdminProvider   *provider.AdminPush
	MaxConfigBytes  int
	CompileTimeout  time.Duration
	Pressure        PressureChecker
}

type Manager struct {
	store           *runtime.Store
	registry        *registry.Registry
	breakerRegistry *breaker.Registry
	outlierRegistry *outlier.Registry
	trafficRegistry *traffic.Registry
	providers       []provider.Provider
	adminProvider   *provider.AdminPush
	maxConfigBytes  int
	compileTimeout  time.Duration
	pressure        PressureChecker
}

type Result struct {
	Snapshot *runtime.Snapshot
	Version  string
	Config   *config.Config
}

func NewManager(cfg ManagerConfig) *Manager {
	return &Manager{
		store:           cfg.Store,
		registry:        cfg.Registry,
		breakerRegistry: cfg.BreakerRegistry,
		outlierRegistry: cfg.OutlierRegistry,
		trafficRegistry: cfg.TrafficRegistry,
		providers:       cfg.Providers,
		adminProvider:   cfg.AdminProvider,
		maxConfigBytes:  cfg.MaxConfigBytes,
		compileTimeout:  cfg.CompileTimeout,
		pressure:        cfg.Pressure,
	}
}

func (m *Manager) Apply(ctx context.Context, raw []byte, source string, mode Mode) (*Result, error) {
	if m == nil {
		return nil, errors.New("apply manager is nil")
	}
	start := time.Now()
	defer func() {
		metrics := obs.DefaultMetrics()
		if metrics == nil {
			return
		}
		metrics.RecordConfigApplyDuration(time.Since(start))
	}()

	maxBytes := m.maxConfigBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxConfigBytes
	}
	if len(raw) > maxBytes {
		return nil, ErrConfigTooLarge
	}
	if m.pressure != nil && m.pressure.UnderPressure() {
		return nil, ErrPressure
	}

	cfg, err := config.ParseJSON(raw)
	if err != nil {
		return nil, err
	}
	version := configVersion(raw)

	var previous *config.Config
	if m.adminProvider != nil {
		previous = m.adminProvider.Swap(cfg)
		if mode == ModeValidate {
			defer m.adminProvider.Swap(previous)
		} else {
			defer func() {
				if err != nil {
					m.adminProvider.Swap(previous)
				}
			}()
		}
	}

	reg := m.registry
	breakerReg := m.breakerRegistry
	outlierReg := m.outlierRegistry
	trafficReg := m.trafficRegistry
	if mode == ModeValidate {
		reg = registry.NewRegistry(0, 0)
		breakerReg = breaker.NewRegistry(0, 0)
		outlierReg = outlier.NewRegistry(0, 0, nil)
		trafficReg = traffic.NewRegistry(0, 0)
		defer reg.Close()
		defer breakerReg.Close()
		defer outlierReg.Close()
		defer trafficReg.Close()
	}

	providers := m.buildProviders(cfg)
	compiled, resolvedCfg, err := m.compile(ctx, providers, reg, breakerReg, outlierReg, trafficReg)
	if err != nil {
		metrics := obs.DefaultMetrics()
		if metrics != nil {
			var conflictErr *provider.ConflictError
			if errors.As(err, &conflictErr) {
				metrics.RecordConfigConflict()
			}
		}
		return nil, err
	}

	compiled.Version = version
	compiled.Source = source

	if mode == ModeApply {
		if m.store != nil {
			if err := m.store.Swap(compiled); err != nil {
				return nil, err
			}
		}
	}

	return &Result{Snapshot: compiled, Version: version, Config: resolvedCfg}, nil
}

func (m *Manager) buildProviders(cfg *config.Config) []provider.Provider {
	providers := make([]provider.Provider, 0, len(m.providers)+1)
	providers = append(providers, m.providers...)
	if m.adminProvider != nil {
		if !containsProvider(providers, m.adminProvider) {
			providers = append(providers, m.adminProvider)
		}
		return providers
	}
	providers = append(providers, &staticProvider{cfg: cfg})
	return providers
}

func configVersion(raw []byte) string {
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func ConfigVersion(raw []byte) string {
	return configVersion(raw)
}

type staticProvider struct {
	cfg *config.Config
}

func (p *staticProvider) Name() string {
	return "admin"
}

func (p *staticProvider) Priority() int {
	return provider.AdminPriority
}

func (p *staticProvider) Load(ctx context.Context) (*config.Config, error) {
	_ = ctx
	return p.cfg, nil
}

func containsProvider(list []provider.Provider, target provider.Provider) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}
