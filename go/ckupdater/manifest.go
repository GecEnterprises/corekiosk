package ckupdater

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
)

// UpdatePolicy is controlled authoritatively by the update server/manifest.
type UpdatePolicy string

const (
	// PolicyImmediate stops any active game and applies the update as soon as it is staged.
	PolicyImmediate UpdatePolicy = "immediate"
	// PolicyWhenIdle stages the update in the background and applies it once the kiosk is in phase "idle".
	PolicyWhenIdle UpdatePolicy = "when_idle"
	// PolicyNextRestart stages the update into ./updates/pending and applies it on the next launcher startup.
	PolicyNextRestart UpdatePolicy = "next_restart"
)

// UpdateTarget identifies an independently distributable artifact.
type UpdateTarget string

const (
	TargetWin64        UpdateTarget = "win64"
	TargetGame         UpdateTarget = "game"
	TargetCompanionWeb UpdateTarget = "companion_web"
	TargetAndroid      UpdateTarget = "android"
)

// TargetArtifact describes one signed downloadable artifact for a specific target.
type TargetArtifact struct {
	Target        UpdateTarget `json:"target"`
	Version       string       `json:"version"`
	Policy        UpdatePolicy `json:"policy"`
	URL           string       `json:"url"`
	StoragePath   string       `json:"storage_path,omitempty"`
	SHA256        string       `json:"sha256"`
	SizeBytes     int64        `json:"size_bytes"`
	KeyID         string       `json:"key_id"`
	Sig           string       `json:"sig"`
	PublishedAtMs int64        `json:"published_at_ms"`
	Notes         string       `json:"notes,omitempty"`
}

// LatestUpdatesResponse is returned by GET /v1/updates/latest.
type LatestUpdatesResponse struct {
	App     string                    `json:"app"`
	Channel string                    `json:"channel"`
	Targets map[string]TargetArtifact `json:"targets"`
}

func canonicalArtifactMessage(app string, a TargetArtifact) []byte {
	policy := a.Policy
	if policy == "" {
		policy = PolicyWhenIdle
	}
	return []byte(fmt.Sprintf("%s|%s|%s|%s|%s", strings.TrimSpace(app), a.Target, a.Version, policy, strings.ToLower(strings.TrimSpace(a.SHA256))))
}

// SignArtifact signs an artifact's canonical tuple (app|target|version|policy|sha256) with Ed25519.
func SignArtifact(priv ed25519.PrivateKey, app string, art *TargetArtifact) {
	pub := priv.Public().(ed25519.PublicKey)
	art.KeyID = KeyIDFromPub(pub)
	sig := ed25519.Sign(priv, canonicalArtifactMessage(app, *art))
	art.Sig = base64.StdEncoding.EncodeToString(sig)
}

// VerifyArtifact verifies an artifact's Ed25519 signature against the trusted public keys map.
func VerifyArtifact(trusted map[string]ed25519.PublicKey, app string, art TargetArtifact) error {
	if len(trusted) == 0 {
		return fmt.Errorf("no trusted release public keys configured")
	}
	pub, ok := trusted[art.KeyID]
	if !ok {
		return fmt.Errorf("untrusted release key_id %q", art.KeyID)
	}
	sigBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(art.Sig))
	if err != nil || len(sigBytes) != ed25519.SignatureSize {
		return fmt.Errorf("invalid ed25519 signature format")
	}
	if !ed25519.Verify(pub, canonicalArtifactMessage(app, art), sigBytes) {
		return fmt.Errorf("ed25519 signature verification failed for %s@%s", art.Target, art.Version)
	}
	return nil
}
