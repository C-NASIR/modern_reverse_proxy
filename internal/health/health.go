package health

import "time"

type Config struct {
	Path                   string
	Interval               time.Duration
	Timeout                time.Duration
	UnhealthyAfterFailures int
	HealthyAfterSuccesses  int
	BaseEjectDuration      time.Duration
	MaxEjectDuration       time.Duration
}
