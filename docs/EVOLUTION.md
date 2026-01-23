# Evolution Boundaries

This project is deliberately scoped. The core is frozen and extensions must preserve its invariants.

## Frozen core

These areas are stable and should not change lightly:

- Snapshot model and swap semantics
- Handler execution order
- Failure categories and error mapping
- Security model (admin separation, signed config rules)
- Admin/data plane separation

## Allowed extensions

Safe additions that do not alter core guarantees:

- New plugins with explicit failure modes
- New cache backends honoring existing cache policy
- New config providers that merge into the snapshot
- New traffic strategies (cohorts, canaries) behind the existing routing model

## Explicit non-goals

- Service mesh replacement
- General purpose API gateway
- Inline business logic or custom transformations
- Stateful request/response transformations

## How to add a feature safely

Use this checklist before merging:

- Does it introduce mutable shared state? If yes, isolate and document it.
- Does it affect the hot path? Keep it bounded and measurable.
- Does it change failure semantics? Update `docs/FAILURE_MODES.md` and add tests.
- Is it per-route or global? Prefer per-route and keep defaults safe.
