package health

type PassiveFailureKind string

const (
	PassiveFailureDial    PassiveFailureKind = "dial"
	PassiveFailureTimeout PassiveFailureKind = "timeout"
)
