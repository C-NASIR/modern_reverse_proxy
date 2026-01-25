#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SECRETS_DIR="${ROOT_DIR}/secrets"
TMP_FILE="$(mktemp)"

mkdir -p "${SECRETS_DIR}"

cat > "${TMP_FILE}" <<'EOF'
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

func main() {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("secrets/signing.key", []byte(base64.StdEncoding.EncodeToString(privateKey)), 0600); err != nil {
		panic(err)
	}
	if err := os.WriteFile("secrets/signing.pub", []byte(base64.StdEncoding.EncodeToString(publicKey)), 0644); err != nil {
		panic(err)
	}
	fmt.Println("wrote secrets/signing.key and secrets/signing.pub")
}
EOF

cd "${ROOT_DIR}"
go run "${TMP_FILE}"
rm -f "${TMP_FILE}"
