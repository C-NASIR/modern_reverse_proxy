package apply

import (
	"context"
	"errors"
	"time"

	"modern_reverse_proxy/internal/breaker"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/outlier"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/traffic"
)

func (m *Manager) ApplyResolved(ctx context.Context, raw []byte, source string, mode Mode) (*Result, error) {
	return m.ApplyResolvedVersion(ctx, raw, source, "", mode)
}

func (m *Manager) ApplyResolvedVersion(ctx context.Context, raw []byte, source string, version string, mode Mode) (*Result, error) {
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
	if version == "" {
		version = configVersion(raw)
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

	snapshot, err := m.compileResolved(ctx, cfg, reg, breakerReg, outlierReg, trafficReg)
	if err != nil {
		return nil, err
	}

	snapshot.Version = version
	snapshot.Source = source

	if mode == ModeApply {
		if m.store != nil {
			m.store.Swap(snapshot)
		}
	}

	return &Result{Snapshot: snapshot, Version: version, Config: cfg}, nil
}

func (m *Manager) compileResolved(ctx context.Context, cfg *config.Config, reg *registry.Registry, breakerReg *breaker.Registry, outlierReg *outlier.Registry, trafficReg *traffic.Registry) (*runtime.Snapshot, error) {
	timeout := m.compileTimeout
	if timeout <= 0 {
		timeout = DefaultCompileTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resultCh := make(chan compileResult, 1)
	go func() {
		snapshot, err := runtime.BuildSnapshot(cfg, reg, breakerReg, outlierReg, trafficReg)
		resultCh <- compileResult{snapshot: snapshot, cfg: cfg, err: err}
	}()

	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ErrCompileTimeout
		}
		return nil, ctx.Err()
	case result := <-resultCh:
		return result.snapshot, result.err
	}
}
