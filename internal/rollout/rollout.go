package rollout

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"modern_reverse_proxy/internal/apply"
	"modern_reverse_proxy/internal/bundle"
	"modern_reverse_proxy/internal/config"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/runtime"
)

type Config struct {
	ApplyManager     *apply.Manager
	Store            *runtime.Store
	Metrics          *obs.Metrics
	LockedBake       time.Duration
	ErrorRateWindow  time.Duration
	ErrorRatePercent float64
}

type Manager struct {
	apply      *apply.Manager
	store      *runtime.Store
	metrics    *obs.Metrics
	lockedBake time.Duration
	gates      *Gates
}

func NewManager(cfg Config) *Manager {
	lockedBake := cfg.LockedBake
	if lockedBake <= 0 {
		lockedBake = time.Minute
	}
	return &Manager{
		apply:      cfg.ApplyManager,
		store:      cfg.Store,
		metrics:    cfg.Metrics,
		lockedBake: lockedBake,
		gates: &Gates{
			Metrics:          cfg.Metrics,
			ErrorRateWindow:  cfg.ErrorRateWindow,
			ErrorRatePercent: cfg.ErrorRatePercent,
		},
	}
}

func (m *Manager) ApplyBundle(ctx context.Context, bundlePayload bundle.Bundle, sourceOverride string) (*apply.Result, error) {
	if m == nil || m.apply == nil {
		return nil, errors.New("rollout manager unavailable")
	}
	configBytes, err := bundlePayload.ConfigBytes()
	if err != nil {
		return nil, err
	}
	source := bundlePayload.Meta.Source
	if sourceOverride != "" {
		source = sourceOverride
	}
	version := bundlePayload.Meta.Version
	previous := (*runtime.Snapshot)(nil)
	if m.store != nil {
		previous = m.store.Get()
	}

	result, err := m.apply.ApplyResolvedVersion(ctx, configBytes, source, version, apply.ModeValidate)
	if err != nil {
		m.recordStage("validate", "error", version, err)
		return nil, err
	}
	m.recordStage("validate", "success", version, nil)

	lockedBytes, err := lockConfigBytes(configBytes)
	if err != nil {
		m.recordStage("locked", "error", version, err)
		return nil, err
	}
	result, err = m.apply.ApplyResolvedVersion(ctx, lockedBytes, source, version, apply.ModeApply)
	if err != nil {
		m.recordStage("locked", "error", version, err)
		return nil, err
	}
	m.recordStage("locked", "success", version, nil)

	if m.lockedBake > 0 {
		timer := time.NewTimer(m.lockedBake)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if err := m.gates.Check(); err != nil {
		m.recordStage("locked", "gated", version, err)
		m.rollback(previous, version)
		return nil, err
	}

	result, err = m.apply.ApplyResolvedVersion(ctx, configBytes, source, version, apply.ModeApply)
	if err != nil {
		m.recordStage("full", "error", version, err)
		m.rollback(previous, version)
		return nil, err
	}
	m.recordStage("full", "success", version, nil)
	return result, nil
}

func (m *Manager) recordStage(stage string, result string, version string, err error) {
	if m.metrics != nil {
		m.metrics.RecordRolloutStage(stage, result)
	}
	if err != nil {
		log.Printf("bundle_version=%s rollout_stage=%s rollout_result=%s reason=%v", version, stage, result, err)
		return
	}
	log.Printf("bundle_version=%s rollout_stage=%s rollout_result=%s", version, stage, result)
}

func (m *Manager) rollback(previous *runtime.Snapshot, targetVersion string) {
	if previous == nil || m.store == nil {
		return
	}
	m.store.Swap(previous)
	if m.metrics != nil {
		m.metrics.RecordRollback("success")
	}
	log.Printf("rollback_from_version=%s rollback_to_version=%s", targetVersion, previous.Version)
}

func lockConfigBytes(raw []byte) ([]byte, error) {
	var cfg config.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	locked := maskLockedConfig(&cfg)
	return json.Marshal(locked)
}

func maskLockedConfig(cfg *config.Config) *config.Config {
	if cfg == nil {
		return nil
	}
	locked := *cfg
	if cfg.Routes != nil {
		locked.Routes = make([]config.Route, len(cfg.Routes))
		for i, route := range cfg.Routes {
			lockedRoute := route
			lockedRoute.Policy.Plugins.Enabled = false
			lockedRoute.Policy.Plugins.Filters = nil
			if lockedRoute.Policy.Traffic.Enabled {
				lockedRoute.Policy.Traffic.CanaryWeight = 0
				if lockedRoute.Policy.Traffic.StableWeight == 0 {
					lockedRoute.Policy.Traffic.StableWeight = 100
				}
			}
			locked.Routes[i] = lockedRoute
		}
	}
	if cfg.Pools != nil {
		locked.Pools = make(map[string]config.Pool, len(cfg.Pools))
		for key, value := range cfg.Pools {
			locked.Pools[key] = value
		}
	}
	return &locked
}
