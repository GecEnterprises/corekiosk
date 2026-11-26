package ckserver

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
)

// ServerIdentity holds the persistent Ed25519 identity keypair for a kiosk server.
type ServerIdentity struct {
	ServerID   string
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
	PubBase64  string
}

type storedKeyFile struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

// DeviceIDFromPub derives a stable 16-hex-char fingerprint (8 bytes of SHA-256) from an Ed25519 public key.
func DeviceIDFromPub(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// LoadOrGenerateIdentity loads a persistent Ed25519 keypair from path, or generates and saves one.
// Passing path == "" generates an ephemeral in-memory keypair (useful for tests).
func LoadOrGenerateIdentity(path string) (*ServerIdentity, error) {
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			var parsed storedKeyFile
			if err := json.Unmarshal(raw, &parsed); err != nil {
				return nil, ckframework.ErrStore.Wrap(err, "decode server key file")
			}
			privBytes, err := base64.StdEncoding.DecodeString(parsed.PrivateKey)
			if err != nil || len(privBytes) != ed25519.PrivateKeySize {
				return nil, ckframework.ErrStore.New("corrupt server private key")
			}
			priv := ed25519.PrivateKey(privBytes)
			pub := priv.Public().(ed25519.PublicKey)
			return &ServerIdentity{
				ServerID:   DeviceIDFromPub(pub),
				PublicKey:  pub,
				PrivateKey: priv,
				PubBase64:  base64.StdEncoding.EncodeToString(pub),
			}, nil
		}
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, ckframework.ErrInternal.Wrap(err, "generate ed25519 server keypair")
	}
	id := &ServerIdentity{
		ServerID:   DeviceIDFromPub(pub),
		PublicKey:  pub,
		PrivateKey: priv,
		PubBase64:  base64.StdEncoding.EncodeToString(pub),
	}
	if path != "" {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, ckframework.ErrStore.Wrap(err, "create key directory")
			}
		}
		payload, _ := json.MarshalIndent(storedKeyFile{
			PrivateKey: base64.StdEncoding.EncodeToString(priv),
			PublicKey:  id.PubBase64,
		}, "", "  ")
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			return nil, ckframework.ErrStore.Wrap(err, "write server key file")
		}
	}
	return id, nil
}
