# Runbook

## 1. How to Run Locally

```bash
chmod +x scripts/*.sh
./scripts/gen-certs.sh
docker compose up -d --build
curl http://localhost:8080/
```

Run the end-to-end smoke test with:

```bash
./scripts/smoke.sh
```

To run the binary directly:

```bash
ADMIN_TOKEN=devtoken \
ADMIN_TLS_CERT_FILE=./secrets/admin-cert.pem \
ADMIN_TLS_KEY_FILE=./secrets/admin-key.pem \
ADMIN_CLIENT_CA_FILE=./secrets/admin-ca.pem \
./bin/proxy -config-file configs/examples/basic.json -http-addr :8080 -admin-addr :9000
```

## 2. How to View Metrics

```bash
curl http://localhost:8080/metrics
```

## 3. How to Push Config

```bash
curl -X POST https://localhost:9000/admin/config \
  --cacert ./secrets/admin-ca.pem \
  --cert ./secrets/admin-client-cert.pem \
  --key ./secrets/admin-client-key.pem \
  -H "Authorization: Bearer devtoken" \
  -H "Content-Type: application/json" \
  --data-binary @configs/examples/cache.json
```

## 4. How to Validate Config

```bash
curl -X POST https://localhost:9000/admin/validate \
  --cacert ./secrets/admin-ca.pem \
  --cert ./secrets/admin-client-cert.pem \
  --key ./secrets/admin-client-key.pem \
  -H "Authorization: Bearer devtoken" \
  -H "Content-Type: application/json" \
  --data-binary @configs/examples/basic.json
```

## 5. How to Apply Signed Bundles

1. Generate signing keys with `./scripts/gen-keys.sh`.
2. Start the proxy with `-public-key-file ./secrets/signing.pub` (or `PUBLIC_KEY_FILE`).
3. Generate a signed bundle (see `internal/bundle` helpers).
4. POST the bundle to `/admin/bundle` with the same mTLS + token setup.

Unsigned apply is blocked when a public key is configured unless `ALLOW_UNSIGNED_ADMIN_CONFIG=true` is set.

## 6. How to Roll Back

```bash
curl -X POST https://localhost:9000/admin/rollback \
  --cacert ./secrets/admin-ca.pem \
  --cert ./secrets/admin-client-cert.pem \
  --key ./secrets/admin-client-key.pem \
  -H "Authorization: Bearer devtoken" \
  -H "Content-Type: application/json" \
  --data-binary '{"version":"<previous-version>"}'
```

Omit the `version` field to roll back to the previous snapshot.

## 7. Common Errors and What They Mean

- `ADMIN_TLS_CERT_FILE`/`ADMIN_TLS_KEY_FILE` missing: admin listener refuses to start.
- `ADMIN_CLIENT_CA_FILE` missing: admin listener refuses to start.
- `unsigned apply disabled`: public key configured and unsigned configs blocked.
- `config_pressure`: snapshot pressure protection returned HTTP 429.
- `tls config missing`: data plane TLS enabled in config but no certs provided.

## 8. Shutdown Procedure

Graceful shutdown is handled by `SIGTERM` (or `docker compose down`). The server drains inflight requests, closes idle connections, and exits after the configured shutdown timeouts.
