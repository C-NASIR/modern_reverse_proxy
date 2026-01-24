# Architecture Overview

### COMPONENT DIAGRAM

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

### Dependency Graph

```mermaid
flowchart LR
  cmd_proxy["cmd/proxy"] --> admin["internal/admin"]
  cmd_proxy --> apply["internal/apply"]
  cmd_proxy --> breaker["internal/breaker"]
  cmd_proxy --> bundle["internal/bundle"]
  cmd_proxy --> cache["internal/cache"]
  cmd_proxy --> config["internal/config"]
  cmd_proxy --> limits["internal/limits"]
  cmd_proxy --> obs["internal/obs"]
  cmd_proxy --> outlier["internal/outlier"]
  cmd_proxy --> plugin["internal/plugin"]
  cmd_proxy --> provider["internal/provider"]
  cmd_proxy --> proxy["internal/proxy"]
  cmd_proxy --> pull["internal/pull"]
  cmd_proxy --> registry["internal/registry"]
  cmd_proxy --> rollout["internal/rollout"]
  cmd_proxy --> runtime["internal/runtime"]
  cmd_proxy --> server["internal/server"]
  cmd_proxy --> traffic["internal/traffic"]

  %% runtime snapshot compilation deps
  runtime --> router["internal/router"]
  runtime --> policy["internal/policy"]
  runtime --> pool["internal/pool"]
  runtime --> transport["internal/transport"]
  runtime --> health["internal/health"]
  runtime --> tlsstore["internal/tlsstore"]
  runtime --> limits
  runtime --> config
  runtime --> registry
  runtime --> breaker
  runtime --> outlier
  runtime --> traffic
  runtime --> plugin

  %% proxy request path deps
  proxy --> runtime
  proxy --> registry
  proxy --> cache
  proxy --> breaker
  proxy --> outlier
  proxy --> plugin
  proxy --> traffic
  proxy --> policy
  proxy --> obs
  proxy --> limits
  proxy --> retry["internal/retry"]

  %% control plane deps
  apply --> config
  apply --> runtime
  apply --> provider
  apply --> registry
  apply --> breaker
  apply --> outlier
  apply --> traffic
  apply --> obs

  admin --> apply
  admin --> rollout
  admin --> runtime

  pull --> rollout
  pull --> bundle
  pull --> runtime

  server --> limits
  server --> runtime

```

### Sequence Diagram

```mermaid
sequenceDiagram
  autonumber
  participant Client
  participant Proxy as Proxy Listener
  participant Handler as proxy.Handler
  participant Store as runtime.Store
  participant Router as router.Router
  participant Engine as proxy.Engine
  participant Registry as registry.Registry
  participant Upstream
  participant Admin as Admin API
  participant Apply as apply.Manager
  participant Build as runtime.BuildSnapshot

  alt Hot path (request handling)
    Client->>Proxy: HTTP/TLS request
    Proxy->>Handler: ServeHTTP
    Handler->>Store: Acquire snapshot
    Store-->>Handler: Snapshot
    Handler->>Router: Match route
    Router-->>Handler: Route + policy
    Handler->>Registry: Pick upstream endpoint
    Registry-->>Handler: Endpoint
    Handler->>Engine: RoundTripWithRetry
    Engine->>Upstream: Forward request
    Upstream-->>Engine: Response
    Engine-->>Handler: Response
    Handler-->>Client: Response
    Handler->>Store: Release snapshot
  end

  alt Control path (config change)
    Client->>Admin: POST /admin/config (new config)
    Admin->>Apply: Validate/Apply
    Apply->>Build: BuildSnapshot
    Build-->>Apply: New snapshot
    Apply->>Store: Swap snapshot
    Store-->>Apply: Swap OK
    Admin-->>Client: 200 OK
  end

```

### Dataflow

```mermaid
flowchart LR
  %% External entities
  Client[Client]
  AdminUser[Admin User]
  ConfigSource[Config Source]

  %% Processes
  P1[Process: Handle Request]
  P2[Process: Compile Snapshot]
  P3[Process: Apply Snapshot]

  %% Data stores
  D1[(Snapshot Store)]
  D2[(Registry State\npools/breakers/outliers)]
  D3[(Cache Store)]
  D4[(Metrics Store)]
  D5[(Upstream Services)]

  %% Data flows: hot path
  Client -->|HTTP/TLS request| P1
  P1 -->|read current snapshot| D1
  P1 -->|use registry state| D2
  P1 -->|cache lookup| D3
  P1 -->|record metrics| D4
  P1 -->|forward request| D5
  D5 -->|response| P1
  P1 -->|HTTP response| Client
  P1 -->|write cache| D3

  %% Data flows: control path
  AdminUser -->|config change| P2
  ConfigSource -->|config JSON| P2
  P2 -->|build snapshot| P3
  P3 -->|swap snapshot| D1
  P3 -->|reconcile state| D2

```

Snapshots are immutable; each config update builds a new snapshot and swaps it atomically. Runtime registries hold shared state such as pools, breakers, and outlier tracking.
