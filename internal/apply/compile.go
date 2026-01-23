package apply

import (
	"context"
	"errors"

	"modern_reverse_proxy/internal/breaker"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/outlier"
	"modern_reverse_proxy/internal/provider"
	"modern_reverse_proxy/internal/registry"
	"modern_reverse_proxy/internal/runtime"
	"modern_reverse_proxy/internal/traffic"
)

type compileResult struct {
	snapshot *runtime.Snapshot
	cfg      *config.Config
	warnings []string
	err      error
}

func (m *Manager) compile(ctx context.Context, providers []provider.Provider, reg *registry.Registry, breakerReg *breaker.Registry, outlierReg *outlier.Registry, trafficReg *traffic.Registry) (*runtime.Snapshot, *config.Config, []string, error) {
	timeout := m.compileTimeout
	if timeout <= 0 {
		timeout = DefaultCompileTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resultCh := make(chan compileResult, 1)
	go func() {
		resolvedCfg, err := provider.Merge(ctx, providers)
		if err != nil {
			resultCh <- compileResult{err: err}
			return
		}
		warnings, err := config.Validate(resolvedCfg)
		if err != nil {
			resultCh <- compileResult{err: err}
			return
		}
		snapshot, err := runtime.BuildSnapshot(resolvedCfg, reg, breakerReg, outlierReg, trafficReg)
		resultCh <- compileResult{snapshot: snapshot, cfg: resolvedCfg, warnings: warnings, err: err}
	}()

	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, nil, nil, ErrCompileTimeout
		}
		return nil, nil, nil, ctx.Err()
	case result := <-resultCh:
		return result.snapshot, result.cfg, result.warnings, result.err
	}
}
