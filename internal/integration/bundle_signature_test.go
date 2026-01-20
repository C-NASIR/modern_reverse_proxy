package integration

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"modern_reverse_proxy/internal/apply"
	"modern_reverse_proxy/internal/bundle"
	"modern_reverse_proxy/internal/testutil"
)

func TestBundleSignatureVerification(t *testing.T) {
	publicKey, privateKey := testutil.GenerateEd25519KeyPair(t)
	configBytes := []byte(`{"listen_addr":"127.0.0.1:0","routes":[],"pools":{}}`)

	meta := bundle.Meta{
		Version:   apply.ConfigVersion(configBytes),
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Source:    "admin",
	}

	signed, err := bundle.NewSignedBundle(configBytes, meta, privateKey)
	if err != nil {
		t.Fatalf("sign bundle: %v", err)
	}

	if err := bundle.VerifyBundle(signed, publicKey); err != nil {
		t.Fatalf("verify bundle: %v", err)
	}

	tampered := signed
	tampered.ConfigBytesB64 = base64.StdEncoding.EncodeToString([]byte(`{"listen_addr":"127.0.0.1:0"}`))

	if err := bundle.VerifyBundle(tampered, publicKey); err == nil {
		t.Fatalf("expected verification failure")
	} else if !errors.Is(err, bundle.ErrBadHash) && !errors.Is(err, bundle.ErrBadSignature) {
		t.Fatalf("unexpected verification error: %v", err)
	}
}
