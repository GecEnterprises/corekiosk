package ckupdater

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReleaseKeyFile is the JSON format saved by `corekiosk keygen`.
type ReleaseKeyFile struct {
	KeyID      string `json:"key_id"`
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
}

// KeyIDFromPub returns the short 8-hex-char fingerprint of an Ed25519 public key.
func KeyIDFromPub(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:4])
}

// EncodePublicKeyBase64 encodes an Ed25519 public key to standard base64.
func EncodePublicKeyBase64(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// GenerateReleaseKeyPair creates a fresh Ed25519 release signing keypair.
func GenerateReleaseKeyPair() (ReleaseKeyFile, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return ReleaseKeyFile{}, err
	}
	return ReleaseKeyFile{
		KeyID:      KeyIDFromPub(pub),
		PublicKey:  base64.StdEncoding.EncodeToString(pub),
		PrivateKey: base64.StdEncoding.EncodeToString(priv),
	}, nil
}

// SaveReleaseKeyFile writes a release keypair JSON file with 0600 permissions.
func SaveReleaseKeyFile(path string, kf ReleaseKeyFile) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// LoadReleaseKeyFile reads a release signing keypair from disk.
func LoadReleaseKeyFile(path string) (ed25519.PrivateKey, ed25519.PublicKey, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, "", err
	}
	var kf ReleaseKeyFile
	if err := json.Unmarshal(raw, &kf); err != nil {
		return nil, nil, "", fmt.Errorf("parse release key file: %w", err)
	}
	return LoadReleaseKeyFileFromStruct(kf)
}

// LoadReleaseKeyFileFromStruct decodes an in-memory ReleaseKeyFile.
func LoadReleaseKeyFileFromStruct(kf ReleaseKeyFile) (ed25519.PrivateKey, ed25519.PublicKey, string, error) {
	privBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(kf.PrivateKey))
	if err != nil || len(privBytes) != ed25519.PrivateKeySize {
		return nil, nil, "", fmt.Errorf("invalid ed25519 private key")
	}
	priv := ed25519.PrivateKey(privBytes)
	pub := priv.Public().(ed25519.PublicKey)
	return priv, pub, KeyIDFromPub(pub), nil
}

// ParseTrustedKeys parses comma-separated base64 Ed25519 public keys into a key_id -> PublicKey map.
func ParseTrustedKeys(csvB64 string) (map[string]ed25519.PublicKey, error) {
	out := make(map[string]ed25519.PublicKey)
	for _, part := range strings.Split(csvB64, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(part)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid trusted release public key %q", part)
		}
		pub := ed25519.PublicKey(raw)
		out[KeyIDFromPub(pub)] = pub
	}
	return out, nil
}
