# Config Guide

The proxy consumes JSON configuration files that define routes, pools, and optional policy. The file is immutable per snapshot; pushing a new config builds and swaps in a new snapshot atomically.

## Top-Level Fields

- `listen_addr`: Legacy field for data plane HTTP. The binary now prefers `-http-addr` and `-tls-addr` flags.
- `tls`: Data plane TLS settings (certs and client CA). TLS is only active when both `tls.enabled` is true and `-tls-addr` is set.
- `limits`: HTTP header/body limits and timeouts.
- `shutdown`: Drain and graceful shutdown timings.
- `logging`: Access log behavior (for example, query redaction).
- `metrics`: Metrics endpoint exposure and token protection settings.
- `routes`: Array of route definitions.
- `pools`: Map of pool name to pool configuration.

## Routes

Each route includes:

- `id`: Unique string.
- `host`: Host header to match (no wildcard support).
- `path_prefix`: URL prefix to match.
- `methods`: Optional list of allowed methods.
- `pool`: Default pool name.
- `policy`: Optional per-route policy overrides (retries, cache, traffic, plugins).

## Pools

Pools define upstream endpoints and health/transport settings.

- `endpoints`: Array of `host:port` upstreams.
- `health`: Optional active health check settings.
- `breaker`: Optional circuit breaker configuration.
- `outlier`: Optional outlier detection settings.
- `transport`: Optional connection pool settings.

## Policy Blocks

- `retry`: Enable retries, attempts, timeouts, and status/error triggers.
- `retry_budget`: Cap retries relative to success volume.
- `client_retry_cap`: Rate-limit retries per client key.
- `cache`: Enable caching with TTL and coalescing.
- `traffic`: Canary routing, cohort routing, overload protection, and autodrain.
- `plugins`: External filter calls (host:port) with fail-open/closed options.

## TLS

Data plane TLS uses `tls.enabled`, `tls.certs`, and optional `tls.client_ca_file`. The listener address comes from the `-tls-addr` flag.

## Logging and Metrics

Use `logging.redact_query` to drop query strings from access logs.

Configure metrics exposure via:

- `metrics.enabled`: Toggle metrics endpoint (default true).
- `metrics.path`: HTTP path to serve metrics (default `/metrics`).
- `metrics.require_token`: Require `Authorization: Bearer <token>` for metrics.
- `metrics.token_env`: Environment variable name for the metrics token (default `METRICS_TOKEN`).

## Examples

Metrics protection and log redaction example:

```json
{
  "logging": {
    "redact_query": true
  },
  "metrics": {
    "enabled": true,
    "path": "/metrics",
    "require_token": true,
    "token_env": "METRICS_TOKEN"
  },
  "routes": [
    {"id": "r1", "host": "example.local", "path_prefix": "/", "pool": "p1"}
  ],
  "pools": {
    "p1": {"endpoints": ["127.0.0.1:8081"]}
  }
}
```

Reference examples in `configs/examples`:

- `basic.json` for a single upstream.
- `retries_breaker.json` for retries + breaker.
- `cache.json` for caching.
- `canary_overload.json` for traffic shaping.
- `plugins.json` for plugin filters.
- `admin_signed.json` for bundle workflows.
- `full.json` for a full configuration showing all available options.

The plugin example assumes a filter service is reachable at the configured address.
