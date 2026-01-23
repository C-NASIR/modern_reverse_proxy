# Security Checklist

Checklist items are binary and must be verified in each environment.

- [ ] Admin listener uses TLS and requires client certs (`ADMIN_TLS_CERT_FILE`, `ADMIN_CLIENT_CA_FILE`).
- [ ] Admin token is set (`ADMIN_TOKEN`) and rotated on schedule.
- [ ] Unsigned apply disabled when `PUBLIC_KEY_FILE` is configured (only allow via `ALLOW_UNSIGNED_ADMIN_CONFIG=true`).
- [ ] Metrics endpoint is protected by a dedicated token or disabled.
- [ ] Access logs redact sensitive headers (`internal/obs/logging.go`).
- [ ] Max body and header limits are configured (`limits` in config).
- [ ] Certificate expiration monitoring is in place for admin and data plane certs.
- [ ] Distributor credentials and signing keys are stored in a secret manager.
- [ ] Plugin failure mode and bypass behavior reviewed for each filter.

## How to validate

```bash
# Admin auth should reject missing client cert
curl -k https://admin.local:9000/admin/snapshot

# Admin auth should accept valid mTLS + token
curl --cert client.pem --key client-key.pem --cacert ca.pem \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://admin.local:9000/admin/snapshot

# Unsigned apply should be blocked when PUBLIC_KEY_FILE is set
curl --cert client.pem --key client-key.pem --cacert ca.pem \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -X POST https://admin.local:9000/admin/config \
  -d @config.json

# Metrics should require its own token (or be disabled)
curl -H "Authorization: Bearer $METRICS_TOKEN" http://proxy.local:8080/metrics
```
