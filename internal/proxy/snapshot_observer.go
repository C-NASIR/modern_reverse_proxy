package proxy

import "modern_reverse_proxy/internal/runtime"

const (
	SnapshotPhaseRouteMatch    = "route_match"
	SnapshotPhaseUpstreamPick  = "upstream_pick"
	SnapshotPhaseResponseWrite = "response_write"
)

type SnapshotObserver interface {
	ObserveSnapshot(phase string, snapshot *runtime.Snapshot)
}
