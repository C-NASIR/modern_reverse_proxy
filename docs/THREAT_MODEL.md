# Threat Model

## Assets

- Admin control of routing and policy (`internal/admin`, `internal/apply`)
- Private traffic and headers handled by the data plane (`internal/proxy/handler.go`)
- Certificates and keys (`internal/tlsstore`, `internal/admin/auth.go`)
- Config bundles and signing keys (`internal/bundle`, `internal/provider`)
- Upstream availability and integrity (`internal/pool`, `internal/health`)

## Attack surfaces

- Data plane listener (`internal/server/server.go`, `internal/proxy/handler.go`)
- Admin listener (`internal/admin`)
- Distributor endpoint (`internal/distributor`)
- Plugin gRPC calls (`internal/plugin`)
- Config file and environment variables (`cmd/proxy/main.go`)
- Metrics endpoint (`internal/obs/metrics.go`)

## Threats and mitigations mapping

- Unauthorized config apply → mTLS + token auth (`internal/admin/auth.go`), unsigned apply blocked when public key configured (`internal/admin/handlers.go`).
- Config tampering in transit → signed bundles verified (`internal/bundle`, `internal/admin/handlers.go`).
- Replay attack on config → version tracking and rollback flow (`internal/runtime`, `internal/admin/handlers.go`).
- Credential leakage via logs → header redaction helper (`internal/obs/logging.go`).
- Slowloris / header abuse → request limits and ReadHeaderTimeout (`internal/limits`, `internal/server/server.go`).
- Request smuggling → strict hop-by-hop header handling (`internal/proxy/handler.go`, `internal/proxy/headers.go`).
- Plugin compromise → timeouts and breaker controls (`internal/plugin`, `internal/policy`).
- Upstream failure cascade → circuit breaker and outlier ejection (`internal/breaker`, `internal/outlier`).

## Residual risks

- Compromised node can still access decrypted traffic in memory.
- Upstream trust assumptions (backend identity and integrity).
- No fleet-wide consistency guarantees across multiple instances.

## Recommended deployment posture

- Run admin listener on a separate network interface and security group.
- Restrict distributor endpoint to internal networks only.
- Rotate admin tokens, client certificates, and signing keys regularly.
- Protect metrics endpoint with a dedicated token or disable it.
