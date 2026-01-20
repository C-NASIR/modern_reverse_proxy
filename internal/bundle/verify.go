package bundle

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
)

var (
	ErrBadHash      = errors.New("bundle hash mismatch")
	ErrBadSignature = errors.New("bundle signature invalid")
)

func VerifyBundle(bundle Bundle, publicKey ed25519.PublicKey) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return ErrBadSignature
	}
	configBytes, err := bundle.ConfigBytes()
	if err != nil {
		return ErrBadHash
	}
	configHashHex, configHashBytes := HashConfig(configBytes)
	if bundle.ConfigSHA256 != configHashHex {
		return ErrBadHash
	}
	metaBytes, err := MetaBytes(bundle.Meta)
	if err != nil {
		return err
	}
	input := signatureInput(metaBytes, configHashBytes)
	sig, err := base64.StdEncoding.DecodeString(bundle.SignatureB64)
	if err != nil {
		return ErrBadSignature
	}
	if !ed25519.Verify(publicKey, input, sig) {
		return ErrBadSignature
	}
	return nil
}
