# Introduction (THIS PROJECT IS IN DEVELOPMENT. 70% FINISHED)

A production-ready reverse proxy built around one principle: **every request executes against a coherent immutable snapshot, and all change happens by building a new snapshot atomically**.

This gives you speed on the hot path and safety during change.

## Why This Proxy Exists

Most reverse proxies either start simple and accrete features until unmaintainable, or try to do everything immediately and collapse under complexity.

Coherent Proxy threads the needle: complete enough for real production needs, constrained enough to actually build and operate.

### Core Design Principle

```
Requests are cheap, deterministic, and cancellable.
Change is safe, validated, and atomic.
```

All runtime mutation lives in long-lived shared components keyed by stable identifiers. Snapshots compile fast lookup structures and contain references to shared state, never owning mutable state directly.

## What It Does

Coherent Proxy sits between clients and services as a shared traffic boundary, doing five jobs simultaneously:

1. **Forward traffic** correctly and efficiently
2. **Route traffic** to the right place
3. **Protect backends** from overload and failures
4. **Prove what happened** when things go wrong
5. **Evolve safely** while traffic continues to flow

## Architecture Overview

### Two Planes

**Data Plane** - Handles every request. Fast, predictable, boring.

**Control Plane** - Changes behavior. Safe, auditable, observable.

### Core Components

```
Client Request
    ↓
TLS Listener (termination, mTLS, HTTP/2)
    ↓
HTTP Listener (timeouts, limits, graceful shutdown)
    ↓
Runtime Snapshot (immutable compiled config)
    ↓
Router (host, path, method, headers matching)
    ↓
Traffic Management (canaries, cohorts, overload protection)
    ↓
Middleware Pipeline (auth, limits, transforms, plugins)
    ↓
Cache Layer (optional, explicit, correctness-first)
    ↓
Upstream Pool (balancing, health, breakers, outliers)
    ↓
Proxy Engine (streaming, retries, error mapping)
    ↓
Upstream Services
```

## Key Features

### Traffic Management

- **Canary deployments** with automatic safety brakes
- **Cohort routing** by header, cookie, or consistent hash
- **Adaptive overload protection** with bounded queuing
- **Hedged requests** for tail latency reduction on safe reads

### Resilience

- **Circuit breakers** per upstream pool with configurable thresholds
- **Outlier detection** and automatic endpoint ejection
- **Retry budgets** to prevent retry storms (10% of successful volume)
- **Health checks** active and passive with fail-open trickle policy

### Safety

- **Zero-downtime deployments** via atomic snapshot swaps
- **Configuration validation** and policy governance before activation
- **Progressive rollouts** with health gates and automatic rollback
- **Audit logging** of all control plane actions

### Observability

- **Structured access logs** with route, upstream, retries, breaker state
- **Per-route and per-pool metrics** with cardinality management
- **Distributed tracing** with spans around all decision points
- **Admin API** for runtime inspection and safe rollback

### Performance

- **Connection pooling** per upstream with HTTP/2 multiplexing
- **Request coalescing** to prevent cache stampedes
- **Streaming** with no unbounded buffering
- **Low latency routing** with compiled match structures

## Quick Start

### Installation

```bash
go install github.com/yourorg/coherent-proxy/cmd/proxy@latest
```

### Basic Configuration

Create `config.yaml`:

```yaml
version: v1

listeners:
  - address: ":8080"
    protocol: http
    read_timeout: 30s
    write_timeout: 30s

routes:
  - id: api-route
    host: api.example.com
    path_prefix: /v1
    pool: api-pool
    policy:
      timeout: 5s
      retries:
        max_attempts: 3
        budget_percent: 10
      rate_limit:
        requests_per_second: 1000

pools:
  - id: api-pool
    endpoints:
      - address: 10.0.1.10:8000
      - address: 10.0.1.11:8000
    balancer: round_robin
    health:
      active:
        interval: 5s
        path: /healthz
      passive:
        consecutive_failures: 5
```

### Run the Proxy

```bash
# Start with file config
proxy --config config.yaml

# Start with dynamic config from admin API
proxy --admin :9090 --static-config static.yaml
```

### Send Traffic

```bash
curl -H "Host: api.example.com" http://localhost:8080/v1/users
```

## Configuration

### Static Configuration

Defines the process itself. Rare changes, may require restart.

```yaml
# Listen addresses
http_address: ":8080"
https_address: ":8443"
admin_address: ":9090"

# Base timeouts
read_header_timeout: 5s
idle_timeout: 90s

# Observability
logging:
  level: info
  format: json
metrics:
  exporter: prometheus
  address: ":9091"
```

### Dynamic Configuration

Defines traffic behavior. Frequent changes, always zero-downtime.

```yaml
version: v1
signature: <base64-signature>

routes: [...]
pools: [...]
policies: [...]
middleware: [...]
certificates: [...]
```

## Components

### Snapshot Model

Every request reads a snapshot pointer once at start. All decisions use that immutable view.

```go
// Conceptual model
type Snapshot struct {
    Version      string
    Router       *CompiledRouter
    Routes       map[RouteID]*Route
    Pools        map[PoolID]*Pool
    Policies     map[PolicyID]*Policy
    Middleware   map[ChainID]*Pipeline
    Certs        *CertStore
    TrafficPlans map[RouteID]*TrafficPlan
}
```

Snapshots reference shared registries for mutable runtime state:

```go
type Registries struct {
    Transports   *TransportRegistry
    Breakers     *BreakerRegistry
    Health       *HealthRegistry
    Outliers     *OutlierRegistry
    RetryBudgets *RetryBudgetRegistry
    Cache        *CacheStoreRegistry
}
```

### Router

Fast compiled matching on host, path, method, headers, query parameters.

```yaml
routes:
  - id: exact-match
    host: api.example.com
    path: /exact/path

  - id: prefix-match
    host: api.example.com
    path_prefix: /api/

  - id: regex-match
    host: ".*\\.example\\.com"
    path_regex: "^/v[0-9]+/.*"

  - id: header-match
    host: api.example.com
    headers:
      X-API-Version: v2
```

### Policy Model

Explicit, never inferred.

```yaml
policies:
  - id: strict-policy
    timeout: 3s
    retries:
      max_attempts: 2
      budget_percent: 10
      idempotent_only: true
    rate_limit:
      requests_per_second: 100
      burst: 20
    circuit_breaker:
      error_threshold_percent: 50
      min_requests: 20
      open_duration: 30s
    body_size_limit: 1MB
    require_tls: true
```

### Upstream Pools

```yaml
pools:
  - id: backend-pool
    endpoints:
      - address: 10.0.1.10:8000
        weight: 100
      - address: 10.0.1.11:8000
        weight: 100

    balancer: weighted_round_robin

    health:
      active:
        interval: 5s
        timeout: 1s
        path: /health
        expected_status: 200-399
      passive:
        consecutive_failures: 5
        ejection_duration: 30s

    circuit_breaker:
      error_threshold_percent: 50
      min_requests: 20
      open_duration: 30s

    outlier_detection:
      consecutive_failures: 5
      error_rate_threshold: 50
      max_ejection_percent: 50
```

### Traffic Management

```yaml
traffic_management:
  - route: api-route
    stable_pool: api-pool-v1
    canary_pool: api-pool-v2
    canary_weight: 10 # 10% to canary

    cohort_routing:
      header: X-Cohort
      mappings:
        beta: api-pool-v2
        stable: api-pool-v1

    overload_protection:
      max_concurrent: 1000
      queue_size: 100
      queue_timeout: 1s
```

### Middleware Pipeline

```yaml
middleware:
  - id: api-chain
    stages:
      - type: request_id
      - type: auth
        config:
          jwt:
            issuer: https://auth.example.com
            audience: api
      - type: rate_limit
        policy_ref: strict-policy
      - type: header_transform
        config:
          add:
            X-Proxy-Version: "1.0"
          remove:
            - X-Internal-Secret
      - type: tracing
      - type: access_log
```

### Cache Layer

Strictly opt-in with correctness guardrails.

```yaml
cache:
  - route: api-route
    enabled: true
    ttl: 60s
    vary_headers:
      - Accept-Language
      - Authorization
    max_size: 10MB

    # Never cache without explicit auth handling
    auth_aware: true

    # Request coalescing for stampede protection
    coalesce: true
    coalesce_timeout: 5s
```

## Advanced Features

### Progressive Rollouts

Control plane manages safe config distribution across fleets.

```yaml
rollout:
  stages:
    - name: canary
      fleet_percent: 1
      bake_time: 5m

    - name: small
      fleet_percent: 10
      bake_time: 10m

    - name: half
      fleet_percent: 50
      bake_time: 15m

    - name: all
      fleet_percent: 100

  health_gates:
    - metric: error_rate
      threshold: 0.01
    - metric: p95_latency
      threshold: 500ms

  auto_rollback:
    error_budget_burn_rate: 10x
```

### Plugins and External Filters

```yaml
plugins:
  - id: custom-auth
    type: external_grpc
    address: localhost:50051
    timeout: 50ms
    fail_mode: closed # or 'open'

  - id: request-logger
    type: builtin
    name: structured_logger
    fail_mode: open
```

### TLS and mTLS

```yaml
tls:
  certificates:
    - cert_file: /etc/certs/api.crt
      key_file: /etc/certs/api.key
      sni: api.example.com

  mtls:
    enabled: true
    ca_file: /etc/certs/ca.crt
    require_client_cert: true

routes:
  - id: admin-route
    require_mtls: true
```

## Admin API

Secure operational interface protected by mTLS and token auth.

### Endpoints

```bash
# Validate config without applying
POST /api/v2/config/validate
Content-Type: application/json
{
  "config": {...}
}

# Apply new config
POST /api/v2/config/apply
Content-Type: application/json
{
  "config": {...}
}

# Diff current vs proposed
POST /api/v2/config/diff
Content-Type: application/json
{
  "config": {...}
}

# Inspect current config
GET /api/v2/config/current

# Inspect pool health
GET /api/v2/pools/{pool_id}/health

# Inspect circuit breaker states
GET /api/v2/breakers

# Inspect canary stats
GET /api/v2/traffic/canary

# Rollback to version
POST /api/v2/config/rollback
Content-Type: application/json
{
  "version": "abc123"
}
```

## Observability

### Metrics

```
# Request metrics (per route, per pool)
proxy_requests_total{route="api-route", pool="api-pool", status="200"}
proxy_request_duration_seconds{route="api-route", pool="api-pool"}
proxy_request_size_bytes{route="api-route"}
proxy_response_size_bytes{route="api-route"}

# Upstream metrics
proxy_upstream_requests_total{pool="api-pool", endpoint="10.0.1.10:8000"}
proxy_upstream_duration_seconds{pool="api-pool", endpoint="10.0.1.10:8000"}

# Health metrics
proxy_pool_healthy_endpoints{pool="api-pool"}
proxy_pool_total_endpoints{pool="api-pool"}

# Circuit breaker metrics
proxy_breaker_state{pool="api-pool", state="open|closed|half_open"}
proxy_breaker_trips_total{pool="api-pool"}

# Retry metrics
proxy_retries_total{route="api-route", outcome="success|exhausted|budget"}
proxy_retry_budget_tokens{route="api-route"}

# Cache metrics
proxy_cache_requests_total{route="api-route", result="hit|miss|bypass"}
proxy_cache_coalesce_waiters{route="api-route"}
```

### Access Logs

Structured JSON with complete request context:

```json
{
  "timestamp": "2026-01-20T10:30:45Z",
  "request_id": "req_abc123",
  "method": "GET",
  "path": "/v1/users/123",
  "host": "api.example.com",
  "route": "api-route",
  "pool": "api-pool",
  "endpoint": "10.0.1.10:8000",
  "status": 200,
  "duration_ms": 45,
  "upstream_duration_ms": 42,
  "retries": 0,
  "cache": "miss",
  "breaker_state": "closed",
  "canary_variant": "stable"
}
```

### Tracing

Distributed traces with spans for:

- Route matching
- Middleware execution
- Upstream selection
- Circuit breaker decisions
- Retry attempts
- Cache operations

Compatible with OpenTelemetry.

## Failure Modes

Every component has explicit failure behavior:

| Component       | Failure                 | Behavior                                  |
| --------------- | ----------------------- | ----------------------------------------- |
| Router          | No route match          | 404 with request_id                       |
| Circuit Breaker | Breaker open            | 503 with X-Proxy-Circuit-Open header      |
| Health Check    | All endpoints unhealthy | Fail-open trickle (1 req/sec)             |
| Cache           | Cache unavailable       | Bypass cache, serve from upstream         |
| Plugin          | Timeout/crash           | Fail open or closed based on config       |
| Upstream        | Connection failed       | 502 with category upstream_connect_failed |
| Upstream        | Timeout                 | 504 with request_id                       |
| Config          | Invalid config          | Reject, keep current snapshot active      |
| Metrics         | Backend slow            | Drop metrics, never block requests        |

## Production Deployment

### Resource Requirements

**Minimum viable:**

- 2 CPU cores
- 4GB RAM
- Handles ~10k requests/sec

**Recommended production:**

- 8 CPU cores
- 16GB RAM
- Handles ~100k requests/sec

**Large scale:**

- 16+ CPU cores
- 32GB+ RAM
- Handles 500k+ requests/sec

### Environment Variables

```bash
# Core
PROXY_CONFIG_FILE=/etc/proxy/config.yaml
PROXY_ADMIN_ADDRESS=:9090
PROXY_LOG_LEVEL=info

# Observability
PROXY_METRICS_ADDRESS=:9091
PROXY_TRACE_ENDPOINT=http://jaeger:14268/api/traces

# TLS
PROXY_TLS_CERT=/etc/certs/proxy.crt
PROXY_TLS_KEY=/etc/certs/proxy.key
```

### Kubernetes Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: coherent-proxy
spec:
  replicas: 3
  template:
    spec:
      containers:
        - name: proxy
          image: coherent-proxy:latest
          ports:
            - containerPort: 8080
              name: http
            - containerPort: 8443
              name: https
            - containerPort: 9090
              name: admin
            - containerPort: 9091
              name: metrics

          livenessProbe:
            httpGet:
              path: /healthz
              port: 9090
            initialDelaySeconds: 10
            periodSeconds: 10

          readinessProbe:
            httpGet:
              path: /ready
              port: 9090
            initialDelaySeconds: 5
            periodSeconds: 5

          resources:
            requests:
              cpu: 2000m
              memory: 4Gi
            limits:
              cpu: 8000m
              memory: 16Gi

          lifecycle:
            preStop:
              exec:
                command: ["/bin/sh", "-c", "sleep 15"]
```

### Graceful Shutdown

The proxy drains cleanly in stages:

1. Mark draining (readiness fails)
2. Stop accepting new connections
3. Wait 10s for LB convergence
4. Close idle upstream connections
5. Wait 30s for active requests
6. Force close remaining connections

## Global Operations

For multi-region deployments:

```
Central Config Authority
    ↓ (signed bundles)
Progressive Rollout Controller
    ↓ (staged deployment)
Regional Distributors (per region)
    ↓ (local fanout)
Proxy Fleets (per region)
```

Features:

- Version control and audit log
- Cryptographic signing of config bundles
- Progressive rollouts with health gates
- Automatic rollback on error budget burn
- Regional autonomy (no cross-region dependencies)

## Implementation Roadmap

Build incrementally without rewrites:

1. ✅ Snapshot model and HTTP proxy engine
2. ✅ Router and route policy objects
3. ✅ Pools, balancers, passive health, retries
4. ✅ Observability (logs, metrics, tracing)
5. ✅ Middleware pipeline and core library
6. ✅ Config manager, snapshot builder, atomic swap
7. ✅ Providers, aggregator, validator, admin API
8. ✅ TLS termination, certificate store, mTLS
9. ✅ Circuit breaker and outlier detection
10. ✅ Traffic management (canaries, overload)
11. ✅ Cache layer with correctness rules
12. ✅ Plugin model and external filters
13. 🚧 Global authority, signed bundles
14. 🚧 Progressive rollouts and audit log

Each step extends the same boundaries instead of changing them.

## Contributing

We welcome contributions! Please:

1. Read `DESIGN.md` for architecture details
2. Check `CONTRIBUTING.md` for guidelines
3. Open an issue before large changes
4. Write tests for all new features
5. Maintain the principle: **immutable snapshots, atomic swaps**

## License

Apache 2.0

## Support

## Philosophy

> A proxy becomes reliable when requests are simple and change is safe.
>
> So we keep the hot path boring.
>
> We push complexity into compilation, validation, governance, and rollouts.
>
> That is how you build a proxy that not only works, but can be trusted by other teams and operated globally without fear.

---

Built with ❤️ for production reliability.
