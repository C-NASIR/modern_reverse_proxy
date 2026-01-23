# Operational Playbooks

## Upstream outage

- Confirm: spikes in `proxy_upstream_errors_total`, `proxy_circuit_open_total`, access logs with `error_category` and `upstream_addr`.
- Immediate actions: shift traffic to healthy pool, disable canary routes, increase timeouts cautiously.
- Rollback if a config change preceded the outage (`snapshot_version` in logs).

## Elevated 5xx

- Confirm: `proxy_requests_total{status_class="5xx"}` and access logs showing `status` with `error_category`.
- Immediate actions: check upstream health, breaker state, and retry budgets; reduce traffic to failing pools.
- Rollback recent config if the spike aligns with `snapshot_version` changes.

## Tail latency spike

- Confirm: p95 from `proxy_request_duration_seconds` and `proxy_upstream_roundtrip_seconds`.
- Check retries (`proxy_retries_total`), outlier ejections, and overload rejects.
- Temporarily lower max inflight or disable heavy plugins for hot routes.

## Cache stampede

- Confirm: `proxy_cache_requests_total` miss surge and `proxy_cache_coalesce_breakaway_total`.
- Enable caching or increase TTL for affected routes.
- Verify coalescing settings and reduce cache bypass rules.

## Config apply failures

- Confirm: admin responses 4xx/5xx and `proxy_config_apply_total{result="rejected"}`.
- Check error payloads for `config_pressure`, size limits, or compile timeouts.
- Use `/admin/validate` before reapplying; rollback if needed.

## Snapshot pressure

- Confirm: admin apply responses with `config_pressure` and rising inflight requests.
- Identify long-running requests in logs (`duration_ms`).
- Increase `max_retired` cautiously or reduce apply frequency.

## Plugin failures

- Confirm: `proxy_plugin_failclosed_total` and `proxy_plugin_bypass_total` spikes.
- Temporarily switch failure mode to fail-open for the affected filter.
- Investigate plugin health separately (gRPC logs and timeouts).

## Certificate expiry or reload issues

- Confirm: TLS handshake failures in logs and admin reload errors.
- Trigger certificate reload and verify with a single canary request.
- Execute rotation plan and monitor `tls` and `mtls_verified` fields in access logs.
