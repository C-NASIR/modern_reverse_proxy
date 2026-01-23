# Design Invariants

This document lists non-negotiable guarantees. Each invariant references the enforcing code path and the regression test that must continue to pass.

## Snapshot invariants

- A request observes exactly one snapshot for its entire lifetime.
  - Enforced by: `internal/proxy/handler.go` (Acquire/Release per request).
  - Test: `internal/integration/invariant_snapshot_test.go` (`TestSnapshotSingleObservation`).
- No request may ever see partially applied configuration.
  - Enforced by: `internal/runtime/store.go` (atomic `Swap`).
  - Test: `internal/integration/snapshot_swap_test.go` (`TestSnapshotSwapAtomicity`).
- Snapshot swaps are atomic and linearizable.
  - Enforced by: `internal/runtime/store.go` (atomic value + mutex).
  - Test: `internal/integration/snapshot_swap_test.go` (`TestSnapshotSwapAtomicity`).
- A snapshot must remain valid until its last reference is released.
  - Enforced by: `internal/runtime/refcount.go` and `internal/runtime/store.go`.
  - Test: `internal/integration/invariant_snapshot_test.go` (`TestRetiredSnapshotLifecycle`).
- Retired snapshots must eventually be reclaimed.
  - Enforced by: `internal/runtime/store.go` (reap on release).
  - Test: `internal/integration/invariant_snapshot_test.go` (`TestRetiredSnapshotLifecycle`).

## Routing invariants

- A request either matches exactly one route or none.
  - Enforced by: `internal/router/router.go` (compiled matcher).
  - Test: `internal/integration/router_policy_test.go` (`TestRouterPolicyMatch`).
- Route matching is deterministic.
  - Enforced by: `internal/router/router.go` (ordered match evaluation).
  - Test: `internal/integration/router_policy_test.go` (`TestRouterPolicyMatch`).
- Route evaluation is side-effect free.
  - Enforced by: `internal/router/router.go` (pure match logic).
  - Test: `internal/integration/router_policy_test.go` (`TestRouterPolicyMatch`).

## Failure isolation invariants

- Plugin failure must not crash the proxy.
  - Enforced by: `internal/proxy/handler.go` (panic recovery + plugin wrappers).
  - Test: `internal/integration/invariant_failure_modes_test.go` (`TestFailureModePluginFailOpenAllowsRequest`).
- Cache failure must not fail a request.
  - Enforced by: `internal/proxy/handler.go` (cache store errors are non-fatal).
  - Test: `internal/integration/invariant_failure_modes_test.go` (`TestFailureModeCacheFailureDoesNotReturn5xx`).
- Health checker failure must not affect request routing.
  - Enforced by: `internal/health/health.go` (active/passive health tracking).
  - Test: `internal/integration/health_active_test.go` (`TestActiveHealthRecoversEndpoint`).
- Breaker state must never affect unrelated routes.
  - Enforced by: `internal/breaker/registry.go` (breaker keyed by pool key).
  - Test: `internal/integration/invariant_no_cross_leak_test.go` (`TestBreakerIsolationAcrossRoutes`).

## Security invariants

- Admin endpoints are unreachable from data plane listeners.
  - Enforced by: `internal/proxy/handler.go` (hard 404 for `/admin/*`).
  - Test: `internal/integration/security_admin_separation_test.go` (`TestAdminNotExposedOnDataPlane`).
- Unsigned config cannot be applied when signature verification is enabled.
  - Enforced by: `internal/admin/handlers.go` (unsigned apply guard).
  - Test: `internal/integration/security_unsigned_apply_disabled_test.go` (`TestUnsignedApplyDisabledWhenPublicKeySet`).
- Sensitive headers are never logged.
  - Enforced by: `internal/obs/logging.go` (header redaction helper).
  - Test: `internal/integration/security_redaction_test.go` (`TestSensitiveHeaderRedaction`).
