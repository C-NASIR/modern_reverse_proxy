#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SECRETS_DIR="${ROOT_DIR}/secrets"

mkdir -p "${SECRETS_DIR}"
cd "${SECRETS_DIR}"

openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout admin-ca.key -out admin-ca.pem -subj "/CN=proxy-admin-ca"

openssl req -newkey rsa:2048 -nodes \
  -keyout admin-key.pem -out admin.csr -subj "/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"

openssl x509 -req -in admin.csr -days 365 \
  -CA admin-ca.pem -CAkey admin-ca.key -CAcreateserial \
  -out admin-cert.pem

openssl req -newkey rsa:2048 -nodes \
  -keyout admin-client-key.pem -out admin-client.csr -subj "/CN=proxy-admin-client"

openssl x509 -req -in admin-client.csr -days 365 \
  -CA admin-ca.pem -CAkey admin-ca.key -CAcreateserial \
  -out admin-client-cert.pem

rm -f admin.csr admin-client.csr admin-ca.srl
