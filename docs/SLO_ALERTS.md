# SLOs and Alert Recommendations

## Core SLOs

1. **Availability**: success rate for proxied requests.
   - Metric: `proxy_requests_total` with `status_class` labels.
   - Target: ≥ 99.9% success for each critical route.

2. **Latency**: p95 request duration for key routes.
   - Metric: `proxy_request_duration_seconds` histogram.
   - Target: p95 below agreed thresholds per route tier.

## Alert recommendations

- High 5xx rate on a route for 5 minutes (based on `proxy_requests_total{status_class="5xx"}`).
- p95 latency above threshold for 10 minutes (based on `proxy_request_duration_seconds`).
- Breaker open rate spike (`proxy_circuit_open_total`).
- Overload rejects nonzero (`proxy_overload_reject_total`).
- Config apply rejections (`proxy_config_apply_total{result="rejected"}`) and admin logs.
- Plugin fail-closed events (`proxy_plugin_failclosed_total`).
- Bundle verification failures (`proxy_bundle_verify_total{result!="ok"}`).

Use route labels to focus on critical paths and apply tighter alert thresholds for high-priority routes.
