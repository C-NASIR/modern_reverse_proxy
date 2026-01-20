package bundle

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"
)

func NewSignedBundle(configBytes []byte, meta Meta, privateKey ed25519.PrivateKey) (Bundle, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return Bundle{}, errors.New("invalid private key")
	}
	if meta.CreatedAt == "" {
		meta.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	configHashHex, configHashBytes := HashConfig(configBytes)
	if meta.Version == "" {
		meta.Version = configHashHex
	}
	metaBytes, err := MetaBytes(meta)
	if err != nil {
		return Bundle{}, err
	}
	input := signatureInput(metaBytes, configHashBytes)
	sig := ed25519.Sign(privateKey, input)

	return Bundle{
		Meta:           meta,
		ConfigBytesB64: base64.StdEncoding.EncodeToString(configBytes),
		ConfigSHA256:   configHashHex,
		SignatureB64:   base64.StdEncoding.EncodeToString(sig),
	}, nil
}

func signatureInput(metaBytes []byte, configHash []byte) []byte {
	combined := append(metaBytes, configHash...)
	sum := sha256.Sum256(combined)
	return sum[:]
}
