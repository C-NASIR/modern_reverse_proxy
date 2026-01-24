# Architecture Overview

```mermaid
flowchart LR
  Client[Clients]

  subgraph DataPlane[Data Plane]
    HTTP[HTTP Listener]
    TLS[TLS Listener]
    Mux[HTTP Mux]
    Handler[proxy.Handler ServeHTTP]
    SnapshotStore[runtime.Store current snapshot]
    Router[router.Router]
    Traffic[traffic.Plan]
    PluginsReq[plugins request]
    Limits[limits]
    Cache[cache layer]
    Breaker[breaker.Registry]
    Outlier[outlier.Registry]
    Retry[retry registry]
    Registry[registry.Registry pools+transports]
    Engine[proxy.Engine]
    Upstreams[Upstream Services]
    Metrics[obs.Metrics + metrics handler]
  end

  subgraph ControlPlane[Control Plane]
    AdminAPI[admin.Handler]
    Auth[admin.Authenticator]
    Apply[apply.Manager]
    Rollout[rollout.Manager]
    Providers[provider admin+file]
    Build[runtime.BuildSnapshot]
    Snapshot[runtime.Snapshot]
    Puller[pull.Puller]
    Bundle[bundle verification]
  end

  Client --> HTTP
  Client --> TLS
  HTTP --> Mux
  TLS --> Mux
  Mux --> Handler
  Handler --> SnapshotStore
  Handler --> Limits
  Handler --> Router
  Router --> Traffic
  Traffic --> PluginsReq
  PluginsReq --> Breaker
  PluginsReq --> Cache
  Cache --> Engine
  Breaker --> Engine
  Outlier --> Engine
  Retry --> Engine
  Engine --> Registry
  Registry --> Upstreams
  Handler --> Metrics
  Mux --> Metrics

  AdminAPI --> Auth
  AdminAPI --> Apply
  Puller --> Rollout
  Rollout --> Apply
  Providers --> Apply
  Apply --> Build
  Build --> Snapshot
  Snapshot --> SnapshotStore

  Build --> Registry
  Build --> Breaker
  Build --> Outlier
  Build --> Traffic

```

Snapshots are immutable; each config update builds a new snapshot and swaps it atomically. Runtime registries hold shared state such as pools, breakers, and outlier tracking.
