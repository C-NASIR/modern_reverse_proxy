#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ADMIN_TOKEN="${ADMIN_TOKEN:-devtoken}"
TEMP_CONFIG="$(mktemp)"

cleanup() {
  docker compose down -v
  rm -f "${TEMP_CONFIG}"
}
trap cleanup EXIT

"${ROOT_DIR}/scripts/gen-certs.sh"

cd "${ROOT_DIR}"
docker compose up -d --build

ready=false
for _ in {1..30}; do
  if curl -fsS http://localhost:8080/metrics >/dev/null; then
    ready=true
    break
  fi
  sleep 1
done

if [ "${ready}" != "true" ]; then
  echo "proxy did not become ready" >&2
  exit 1
fi

curl -fsS http://localhost:8080/ | grep -q "upstream_a"

cat > "${TEMP_CONFIG}" <<'EOF'
{
  "routes": [
    {
      "id": "smoke",
      "host": "localhost",
      "path_prefix": "/",
      "pool": "primary"
    }
  ],
  "pools": {
    "primary": {
      "endpoints": ["upstream_b:8082"],
      "health": {
        "path": "/",
        "interval_ms": 2000,
        "timeout_ms": 1000
      }
    }
  }
}
EOF

curl -fsS https://localhost:9000/admin/validate \
  --cacert "${ROOT_DIR}/secrets/admin-ca.pem" \
  --cert "${ROOT_DIR}/secrets/admin-client-cert.pem" \
  --key "${ROOT_DIR}/secrets/admin-client-key.pem" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  -H "Content-Type: application/json" \
  --data-binary "@${TEMP_CONFIG}"

curl -fsS https://localhost:9000/admin/config \
  --cacert "${ROOT_DIR}/secrets/admin-ca.pem" \
  --cert "${ROOT_DIR}/secrets/admin-client-cert.pem" \
  --key "${ROOT_DIR}/secrets/admin-client-key.pem" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  -H "Content-Type: application/json" \
  --data-binary "@${TEMP_CONFIG}"

swapped=false
for _ in {1..20}; do
  if curl -fsS http://localhost:8080/ | grep -q "upstream_b"; then
    swapped=true
    break
  fi
  sleep 1
done

if [ "${swapped}" != "true" ]; then
  echo "config swap did not take effect" >&2
  exit 1
fi

curl -fsS http://localhost:8080/metrics | grep -q "proxy_requests_total"
