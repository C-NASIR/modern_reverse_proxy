package testutil

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

type KeyPair struct {
	PublicKey      ed25519.PublicKey
	PrivateKey     ed25519.PrivateKey
	PublicKeyFile  string
	PrivateKeyFile string
}

func GenerateEd25519KeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	return publicKey, privateKey
}

func WriteEd25519KeyPair(t *testing.T, name string) KeyPair {
	t.Helper()
	publicKey, privateKey := GenerateEd25519KeyPair(t)

	dir := t.TempDir()
	publicPath := filepath.Join(dir, name+"_public.key")
	privatePath := filepath.Join(dir, name+"_private.key")

	publicEncoded := base64.StdEncoding.EncodeToString(publicKey)
	privateEncoded := base64.StdEncoding.EncodeToString(privateKey)

	if err := os.WriteFile(publicPath, []byte(publicEncoded), 0600); err != nil {
		t.Fatalf("write public key: %v", err)
	}
	if err := os.WriteFile(privatePath, []byte(privateEncoded), 0600); err != nil {
		t.Fatalf("write private key: %v", err)
	}

	return KeyPair{
		PublicKey:      publicKey,
		PrivateKey:     privateKey,
		PublicKeyFile:  publicPath,
		PrivateKeyFile: privatePath,
	}
}
