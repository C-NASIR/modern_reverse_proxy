package bundle

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
)

func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := parseKeyData(bytes.TrimSpace(data), ed25519.PublicKeySize)
	if err != nil {
		return nil, err
	}
	return ed25519.PublicKey(key), nil
}

func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := parseKeyData(bytes.TrimSpace(data), ed25519.PrivateKeySize)
	if err != nil {
		return nil, err
	}
	return ed25519.PrivateKey(key), nil
}

func parseKeyData(data []byte, size int) ([]byte, error) {
	if len(data) == size {
		return data, nil
	}
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(data)))
	n, err := base64.StdEncoding.Decode(decoded, data)
	if err != nil {
		return nil, err
	}
	decoded = decoded[:n]
	if len(decoded) != size {
		return nil, errors.New("invalid key size")
	}
	return decoded, nil
}
