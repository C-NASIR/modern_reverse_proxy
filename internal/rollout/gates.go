package rollout

import (
	"errors"
	"fmt"
	"time"

	"modern_reverse_proxy/internal/obs"
)

var ErrGateFailed = errors.New("rollout gate failed")

type Gates struct {
	Metrics          *obs.Metrics
	ErrorRateWindow  time.Duration
	ErrorRatePercent float64
}

func (g *Gates) Check() error {
	if g == nil || g.Metrics == nil {
		return nil
	}
	window := g.ErrorRateWindow
	if window <= 0 {
		window = 10 * time.Second
	}
	percent := g.ErrorRatePercent
	if percent <= 0 {
		percent = 1
	}
	total, errorsCount := g.Metrics.Rolling5xx(window)
	if total == 0 {
		return nil
	}
	rate := (float64(errorsCount) / float64(total)) * 100
	if rate > percent {
		return fmt.Errorf("%w: error rate %.2f%%", ErrGateFailed, rate)
	}
	return nil
}
