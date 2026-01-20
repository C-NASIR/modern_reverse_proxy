package bundle

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
)

type Meta struct {
	Version   string `json:"version"`
	CreatedAt string `json:"created_at"`
	Source    string `json:"source"`
	Notes     string `json:"notes,omitempty"`
}

type Bundle struct {
	Meta           Meta   `json:"meta"`
	ConfigBytesB64 string `json:"config_bytes_b64"`
	ConfigSHA256   string `json:"config_sha256"`
	SignatureB64   string `json:"signature_b64"`
}

func (b Bundle) ConfigBytes() ([]byte, error) {
	if b.ConfigBytesB64 == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(b.ConfigBytesB64)
}

func (b Bundle) ConfigHashBytes() ([]byte, error) {
	if b.ConfigSHA256 == "" {
		return nil, nil
	}
	return hex.DecodeString(b.ConfigSHA256)
}

func MetaBytes(meta Meta) ([]byte, error) {
	return json.Marshal(meta)
}

func HashConfig(raw []byte) (string, []byte) {
	checksum := sha256.Sum256(raw)
	return hex.EncodeToString(checksum[:]), checksum[:]
}
