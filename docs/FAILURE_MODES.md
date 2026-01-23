# Failure Modes

Each category below is a frozen contract. If behavior changes, update this document and its regression tests together.

## no_route

- HTTP status: 404
- Retryable: no
- Client body: JSON error
- When it occurs: router finds no matching route.
- Must not happen: upstream contact.

## overloaded

- HTTP status: 503
- Retryable: yes (after retry-after)
- Client body: JSON error
- When it occurs: overload limiter rejects before queue admission.
- Must not happen: upstream contact.

## request_timeout

- HTTP status: 504
- Retryable: yes
- Client body: JSON error
- When it occurs: request context exceeds the per-route request timeout.
- Must not happen: retries beyond budget.

## upstream_timeout

- HTTP status: 504
- Retryable: yes (if retry policy allows)
- Client body: JSON error
- When it occurs: upstream roundtrip exceeds timeout or deadline.
- Must not happen: downstream timeout mislabeled.

## upstream_connect_failed

- HTTP status: 502
- Retryable: yes (if retry policy allows)
- Client body: JSON error
- When it occurs: dial/connect error reaching upstream.
- Must not happen: request treated as success.

## circuit_open

- HTTP status: 503
- Retryable: yes
- Client body: JSON error
- When it occurs: breaker is open for the route’s pool key.
- Must not happen: upstream contacted or retries attempted.

## plugin_timeout

- HTTP status: 503
- Retryable: no
- Client body: JSON error
- When it occurs: plugin call times out in fail-closed mode.
- Must not happen: upstream contacted.

## plugin_unavailable

- HTTP status: 503
- Retryable: no
- Client body: JSON error
- When it occurs: plugin connection fails in fail-closed mode.
- Must not happen: upstream contacted.

## mtls_required

- HTTP status: 403
- Retryable: no
- Client body: JSON error
- When it occurs: route requires mTLS and client cert is missing/invalid.
- Must not happen: 401 or 500 for this path.

## request_too_large

- HTTP status: 413
- Retryable: no
- Client body: JSON error
- When it occurs: request body exceeds limit or is too large to parse.
- Must not happen: upstream contacted.

## headers_too_large

- HTTP status: 431
- Retryable: no
- Client body: JSON error
- When it occurs: header count exceeds configured limit.
- Must not happen: upstream contacted.

## uri_too_long

- HTTP status: 414
- Retryable: no
- Client body: JSON error
- When it occurs: URL length exceeds configured limit.
- Must not happen: upstream contacted.

## config_pressure

- HTTP status: 429 (admin apply)
- Retryable: yes (after pressure clears)
- Client body: JSON error
- When it occurs: snapshot pressure rejects config apply.
- Must not happen: partial apply.
